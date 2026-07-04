package triage

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
)

// mkTeam 建一个 Team。
func mkTeam(t *testing.T, c *ent.Client, slug string) *ent.Team {
	t.Helper()
	tm, err := c.Team.Create().SetName(slug).SetSlug(slug).Save(context.Background())
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	return tm
}

// mkService 建一个 active Service（可带 labels）。
func mkService(t *testing.T, c *ent.Client, teamID int, name, slug string, labels map[string]string) *ent.Service {
	t.Helper()
	b := c.Service.Create().SetName(name).SetSlug(slug).SetTeamID(teamID).SetAutoCreateIncident(true)
	if labels != nil {
		b.SetLabels(labels)
	}
	svc, err := b.Save(context.Background())
	if err != nil {
		t.Fatalf("create service %s: %v", slug, err)
	}
	return svc
}

// mkEvent 建一个 firing Event（指定 labels）。
func mkEvent(t *testing.T, c *ent.Client, dedupKey string, labels map[string]string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusFiring).
		SetSummary("告警 " + dedupKey).
		SetLabels(labels).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// TestRoute_SlugBackwardCompat 保证向后兼容：labels["service"]=slug 仍直达匹配。
func TestRoute_SlugBackwardCompat(t *testing.T) {
	c := newTestClient(t)
	tm := mkTeam(t, c, "pay")
	mkService(t, c, tm.ID, "payment-api", "payment", nil)
	eng := NewEngine(c, nil)

	evt := mkEvent(t, c, "k1", map[string]string{"service": "payment"})
	svc, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if svc == nil || svc.Slug != "payment" {
		t.Fatalf("slug route: got %v, want payment", svc)
	}
}

// TestRoute_LabelSubsetMatch Event.labels ⊇ Service.labels 时命中（子集匹配）。
func TestRoute_LabelSubsetMatch(t *testing.T) {
	c := newTestClient(t)
	tm := mkTeam(t, c, "pay")
	// Service 要求 env=prod（单标签）。
	svc := mkService(t, c, tm.ID, "payment-api", "payment", map[string]string{"env": "prod"})
	eng := NewEngine(c, nil)

	// Event 带 env=prod + 额外标签 → 超集，命中。
	evt := mkEvent(t, c, "k1", map[string]string{"env": "prod", "region": "cn"})
	got, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got == nil || got.ID != svc.ID {
		t.Fatalf("subset match: got %v, want svc %d", got, svc.ID)
	}

	// Event 缺 env → 不命中。
	evt2 := mkEvent(t, c, "k2", map[string]string{"region": "cn"})
	got2, err := eng.route(context.Background(), evt2)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got2 != nil {
		t.Fatalf("missing label should not match, got %v", got2)
	}
}

// TestRoute_GlobMatch Service.labels 值支持 glob（path.Match）。
func TestRoute_GlobMatch(t *testing.T) {
	c := newTestClient(t)
	tm := mkTeam(t, c, "pay")
	svc := mkService(t, c, tm.ID, "payment-api", "payment", map[string]string{"env": "prod-*"})
	eng := NewEngine(c, nil)

	evt := mkEvent(t, c, "k1", map[string]string{"env": "prod-cn"})
	got, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got == nil || got.ID != svc.ID {
		t.Fatalf("glob match: got %v, want svc %d", got, svc.ID)
	}

	// 不匹配 glob → 未命中。
	evt2 := mkEvent(t, c, "k2", map[string]string{"env": "staging"})
	got2, err := eng.route(context.Background(), evt2)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got2 != nil {
		t.Fatalf("non-matching glob should not match, got %v", got2)
	}
}

// TestRoute_MostSpecificWins 多命中时匹配标签数多的（更具体的）Service 优先，确定性裁决。
func TestRoute_MostSpecificWins(t *testing.T) {
	c := newTestClient(t)
	tm := mkTeam(t, c, "pay")
	// 宽松：只要 env=prod（1 标签）。
	broad := mkService(t, c, tm.ID, "broad", "broad", map[string]string{"env": "prod"})
	// 具体：env=prod + tier=1（2 标签）。
	specific := mkService(t, c, tm.ID, "specific", "specific", map[string]string{"env": "prod", "tier": "1"})
	eng := NewEngine(c, nil)

	// Event 两者都满足 → 应命中更具体的 specific。
	evt := mkEvent(t, c, "k1", map[string]string{"env": "prod", "tier": "1"})
	got, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got == nil || got.ID != specific.ID {
		t.Fatalf("most specific wins: got %v, want specific %d (broad=%d)", got, specific.ID, broad.ID)
	}

	// 只满足 broad 的条件 → 命中 broad。
	evt2 := mkEvent(t, c, "k2", map[string]string{"env": "prod", "tier": "2"})
	got2, err := eng.route(context.Background(), evt2)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got2 == nil || got2.ID != broad.ID {
		t.Fatalf("broad match: got %v, want broad %d", got2, broad.ID)
	}
}

// TestRoute_EmptyLabelsServiceIgnored 无 labels 的 Service 不参与子集匹配（避免空规则匹配一切）。
func TestRoute_EmptyLabelsServiceIgnored(t *testing.T) {
	c := newTestClient(t)
	tm := mkTeam(t, c, "pay")
	mkService(t, c, tm.ID, "no-labels", "nolabels", nil) // 无 labels
	eng := NewEngine(c, nil)

	evt := mkEvent(t, c, "k1", map[string]string{"env": "prod"})
	got, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got != nil {
		t.Fatalf("empty-labels service must not match, got %v", got)
	}
}

// TestRoute_IntegrationDefaultFallback B14：label 均未命中时回退 Integration 默认 service。
func TestRoute_IntegrationDefaultFallback(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	svc := mkService(t, c, tm.ID, "default-svc", "defaultsvc", nil)
	// Integration 预设默认 service。
	integ, err := c.Integration.Create().
		SetName("prom-integ").
		SetType("prometheus").
		SetToken("tok-1").
		SetService(svc).
		Save(ctx)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	// Event 关联 Integration、labels 不匹配任何 Service。
	evt, err := c.Event.Create().
		SetSourceEventID("k1").SetSource("prometheus").
		SetSeverity(event.SeverityWarning).SetStatus(event.StatusFiring).
		SetSummary("孤儿").SetLabels(map[string]string{"foo": "bar"}).
		SetDedupKey("k1").SetIntegration(integ).
		Save(ctx)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	eng := NewEngine(c, nil)
	got, err := eng.route(ctx, evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got == nil || got.ID != svc.ID {
		t.Fatalf("integration default fallback: got %v, want svc %d", got, svc.ID)
	}
}

// TestRoute_NoIntegrationNoMatch 无 Integration 且 label 未命中 → nil（unrouted）。
func TestRoute_NoIntegrationNoMatch(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil)
	evt := mkEvent(t, c, "k1", map[string]string{"foo": "bar"})
	got, err := eng.route(context.Background(), evt)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got != nil {
		t.Fatalf("no match no integration should be unrouted, got %v", got)
	}
}

// TestReroute_UnroutedToService M6：把未路由 Event 指派到 Service 并建单。
func TestReroute_UnroutedToService(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	svc := mkService(t, c, tm.ID, "payment-api", "payment", nil)
	eng := NewEngine(c, nil)

	// 未路由 Event（labels 不命中）。
	evt := mkEvent(t, c, "k1", map[string]string{"foo": "bar"})
	res, err := eng.Reroute(ctx, evt.ID, svc.ID)
	if err != nil {
		t.Fatalf("Reroute: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("reroute action: got %q, want incident_created", res.Action)
	}
	// Event 现在应绑定该 Service。
	bound, err := evt.QueryService().Only(ctx)
	if err != nil || bound.ID != svc.ID {
		t.Fatalf("event should be bound to svc %d, got %v err=%v", svc.ID, bound, err)
	}
}

// TestReroute_AlreadyRouted 已路由的 Event 拒绝重路由（返回 ErrRerouteAlreadyRouted）。
func TestReroute_AlreadyRouted(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	svc := mkService(t, c, tm.ID, "payment-api", "payment", nil)
	other := mkService(t, c, tm.ID, "other", "other", nil)
	eng := NewEngine(c, nil)

	evt := mkEvent(t, c, "k1", map[string]string{"foo": "bar"})
	// 先绑定到 svc。
	if err := c.Event.UpdateOneID(evt.ID).SetServiceID(svc.ID).Exec(ctx); err != nil {
		t.Fatalf("bind: %v", err)
	}
	_, err := eng.Reroute(ctx, evt.ID, other.ID)
	if !errors.Is(err, ErrRerouteAlreadyRouted) {
		t.Fatalf("reroute already-routed: got err %v, want ErrRerouteAlreadyRouted", err)
	}
}

// TestSetWindows C9：SetWindows 覆盖去重/聚合窗口，<=0 保留原值。
func TestSetWindows(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil)
	if eng.dedupWindow != defaultDedupWindow || eng.aggregateWindow != defaultAggregateWindow {
		t.Fatalf("default windows wrong: dedup=%v agg=%v", eng.dedupWindow, eng.aggregateWindow)
	}
	eng.SetWindows(0, 0) // 全 <=0 → 保留默认
	if eng.dedupWindow != defaultDedupWindow || eng.aggregateWindow != defaultAggregateWindow {
		t.Fatalf("zero windows should keep defaults")
	}
	custom := defaultDedupWindow * 3
	eng.SetWindows(custom, custom*2)
	if eng.dedupWindow != custom || eng.aggregateWindow != custom*2 {
		t.Fatalf("custom windows not applied: dedup=%v agg=%v", eng.dedupWindow, eng.aggregateWindow)
	}
}
