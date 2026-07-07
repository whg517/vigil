package triage

import (
	"context"
	"regexp"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/service"
)

// setTeamDefaultPolicy 给团队建一个升级策略并设为其 default_escalation_policy（方案C 前置）。
func setTeamDefaultPolicy(t *testing.T, c *ent.Client, teamID int) *ent.EscalationPolicy {
	t.Helper()
	ctx := context.Background()
	pol, err := c.EscalationPolicy.Create().
		SetName("def-pol").SetRepeatTimes(0).
		SetLevels([]schema.EscalationLevel{}).SetTeamID(teamID).Save(ctx)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := c.Team.UpdateOneID(teamID).SetDefaultEscalationPolicyID(pol.ID).Exec(ctx); err != nil {
		t.Fatalf("set default policy: %v", err)
	}
	return pol
}

func countServices(t *testing.T, c *ent.Client) int {
	t.Helper()
	n, err := c.Service.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count services: %v", err)
	}
	return n
}

// TestAutoProvision_Disabled_NoRegression 开关关闭时行为与今天完全一致：未路由，不建服务。
func TestAutoProvision_Disabled_NoRegression(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil) // 未调用 SetAutoProvision → 关闭

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Fatalf("disabled auto-provision: got action %q, want unrouted", res.Action)
	}
	if n := countServices(t, c); n != 0 {
		t.Fatalf("disabled auto-provision must not create service, got %d", n)
	}
}

// TestAutoProvision_CreatesService 满足全部条件时创建 source=auto 服务、继承团队默认策略、建单。
func TestAutoProvision_CreatesService(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	pol := setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("auto-provision: got action %q, want incident_created", res.Action)
	}
	// 服务创建正确：source=auto、provisioned_at 有值、归属团队、继承默认策略。
	svc, err := c.Service.Query().Where(service.SlugEQ("newsvc")).Only(ctx)
	if err != nil {
		t.Fatalf("query provisioned service: %v", err)
	}
	if svc.Source != service.SourceAuto {
		t.Fatalf("source: got %q, want auto", svc.Source)
	}
	if svc.ProvisionedAt == nil {
		t.Fatalf("provisioned_at must be set for auto service")
	}
	if svc.Status != service.StatusActive {
		t.Fatalf("auto service should be active, got %q", svc.Status)
	}
	// 跨团队隔离：服务与其 Incident 都归属解析出的团队，不越权落到别处。
	svcTeam, err := svc.QueryTeam().Only(ctx)
	if err != nil || svcTeam.ID != tm.ID {
		t.Fatalf("service team: got %v err=%v, want team %d", svcTeam, err, tm.ID)
	}
	boundPol, err := svc.QueryEscalationPolicy().Only(ctx)
	if err != nil || boundPol.ID != pol.ID {
		t.Fatalf("service escalation policy: got %v err=%v, want pol %d", boundPol, err, pol.ID)
	}
	inc, err := c.Incident.Query().Where(incident.IDEQ(res.IncidentID)).WithTeam().Only(ctx)
	if err != nil {
		t.Fatalf("query incident: %v", err)
	}
	if inc.Edges.Team == nil || inc.Edges.Team.ID != tm.ID {
		t.Fatalf("incident team: got %v, want team %d", inc.Edges.Team, tm.ID)
	}
}

// TestAutoProvision_NoServiceLabel_Unrouted 无服务键 label → 不建服务，回落 unrouted。
func TestAutoProvision_NoServiceLabel_Unrouted(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	evt := mkEvent(t, c, "k1", map[string]string{"team": "pay"}) // 无 service 键
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Fatalf("no service label: got %q, want unrouted", res.Action)
	}
	if n := countServices(t, c); n != 0 {
		t.Fatalf("no service label must not create service, got %d", n)
	}
}

// TestAutoProvision_NoDefaultPolicy_Unrouted 团队无默认策略 → 不建服务（避免无策略静默）。
func TestAutoProvision_NoDefaultPolicy_Unrouted(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	mkTeam(t, c, "pay") // 团队存在但未设默认策略
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Fatalf("no default policy: got %q, want unrouted", res.Action)
	}
	if n := countServices(t, c); n != 0 {
		t.Fatalf("no default policy must not create service, got %d", n)
	}
}

// TestAutoProvision_TeamUnresolved_Unrouted 团队解析不到（无 team 标签、无兜底团队）→ 不建服务。
func TestAutoProvision_TeamUnresolved_Unrouted(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil) // 无兜底团队

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc"}) // 无 team 标签
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Fatalf("team unresolved: got %q, want unrouted", res.Action)
	}
	if n := countServices(t, c); n != 0 {
		t.Fatalf("team unresolved must not create service, got %d", n)
	}
}

// TestAutoProvision_DefaultTeamFallback 无 team 标签时回退配置的兜底团队。
func TestAutoProvision_DefaultTeamFallback(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "infra")
	setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "infra", nil) // 兜底团队 infra

	evt := mkEvent(t, c, "k1", map[string]string{"service": "mw-redis"}) // 无 team 标签
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("default team fallback: got %q, want incident_created", res.Action)
	}
	svc, err := c.Service.Query().Where(service.SlugEQ("mw-redis")).WithTeam().Only(ctx)
	if err != nil {
		t.Fatalf("query service: %v", err)
	}
	if svc.Edges.Team == nil || svc.Edges.Team.ID != tm.ID {
		t.Fatalf("service should belong to default team %d, got %v", tm.ID, svc.Edges.Team)
	}
}

// TestAutoProvision_SlugPatternReject slug 不过白名单 → 不建服务。
func TestAutoProvision_SlugPatternReject(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil)
	// 仅允许 svc- 前缀的服务名。
	eng.SetAutoProvision(true, "service", "team", "", regexp.MustCompile(`^svc-`))

	// 不匹配白名单 → unrouted。
	evt := mkEvent(t, c, "k1", map[string]string{"service": "up", "team": "pay"})
	res, err := eng.Process(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Fatalf("slug rejected: got %q, want unrouted", res.Action)
	}
	if n := countServices(t, c); n != 0 {
		t.Fatalf("rejected slug must not create service, got %d", n)
	}

	// 匹配白名单 → 建服务。
	evt2 := mkEvent(t, c, "k2", map[string]string{"service": "svc-pay", "team": "pay"})
	res2, err := eng.Process(ctx, evt2.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res2.Action != ActionIncidentCreated {
		t.Fatalf("allowed slug: got %q, want incident_created", res2.Action)
	}
}

// TestAutoProvision_Idempotent 同名服务的多条告警只创建一个 Service（幂等）。
func TestAutoProvision_Idempotent(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	// 第一条：自动供给建服务。
	e1 := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	if _, err := eng.Process(ctx, e1.ID); err != nil {
		t.Fatalf("Process e1: %v", err)
	}
	// 第二条：同名——route 直达既有服务（不再重复供给）。
	e2 := mkEvent(t, c, "k2", map[string]string{"service": "newsvc", "team": "pay"})
	if _, err := eng.Process(ctx, e2.ID); err != nil {
		t.Fatalf("Process e2: %v", err)
	}
	if n := countServices(t, c); n != 1 {
		t.Fatalf("idempotent auto-provision: got %d services, want 1", n)
	}
}

// TestTryAutoProvision_RespectsDisabledExisting 已存在同名但被停用的服务 → 不复活（返回 nil）。
func TestTryAutoProvision_RespectsDisabledExisting(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	// 预建一个 disabled 的同名服务（模拟人工停用）。
	if _, err := c.Service.Create().SetName("newsvc").SetSlug("newsvc").
		SetTeamID(tm.ID).SetStatus(service.StatusDisabled).Save(ctx); err != nil {
		t.Fatalf("pre-create disabled service: %v", err)
	}
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	got, err := eng.tryAutoProvision(ctx, evt)
	if err != nil {
		t.Fatalf("tryAutoProvision: %v", err)
	}
	if got != nil {
		t.Fatalf("must respect disabled existing service, got %v", got)
	}
	// 仍只有那一个 disabled 服务，未新建。
	if n := countServices(t, c); n != 1 {
		t.Fatalf("must not create duplicate, got %d services", n)
	}
}

// TestTryAutoProvision_ReusesActiveExisting 直接调用命中既有 active 同名服务时复用（幂等分支）。
func TestTryAutoProvision_ReusesActiveExisting(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm := mkTeam(t, c, "pay")
	setTeamDefaultPolicy(t, c, tm.ID)
	existing := mkService(t, c, tm.ID, "newsvc", "newsvc", nil) // active
	eng := NewEngine(c, nil)
	eng.SetAutoProvision(true, "service", "team", "", nil)

	evt := mkEvent(t, c, "k1", map[string]string{"service": "newsvc", "team": "pay"})
	got, err := eng.tryAutoProvision(ctx, evt)
	if err != nil {
		t.Fatalf("tryAutoProvision: %v", err)
	}
	if got == nil || got.ID != existing.ID {
		t.Fatalf("should reuse existing active service %d, got %v", existing.ID, got)
	}
	if n := countServices(t, c); n != 1 {
		t.Fatalf("should not duplicate, got %d services", n)
	}
}
