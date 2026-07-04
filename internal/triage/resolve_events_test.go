package triage

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"
)

// createFiringEvent 建一条 firing Event（labels.service=payment），可指定 dedupKey。
func createFiringEvent(t *testing.T, c *ent.Client, dedupKey string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusFiring).
		SetSummary("支付服务告警 " + dedupKey).
		SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create firing event: %v", err)
	}
	return evt
}

// createResolvedEvent 建一条 resolved Event，dedupKey 与对应 firing 相同（真实场景共用指纹）。
func createResolvedEvent(t *testing.T, c *ent.Client, dedupKey string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusResolved).
		SetSummary("支付服务告警恢复 " + dedupKey).
		SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create resolved event: %v", err)
	}
	return evt
}

// engineWithBusAndRecorder 构造带事件总线 + 时间线记录器的引擎，返回引擎与捕获到的事件切片指针。
func engineWithBusAndRecorder(t *testing.T, c *ent.Client) (*Engine, *[]domainevent.Event) {
	t.Helper()
	bus := domainevent.New()
	var captured []domainevent.Event
	bus.Subscribe(domainevent.IncidentResolved, func(_ context.Context, e domainevent.Event) error {
		captured = append(captured, e)
		return nil
	})
	eng := NewEngine(c, nil)
	eng.SetBus(bus)
	eng.SetRecorder(timeline.NewRecorder(c))
	return eng, &captured
}

// countTimeline 统计某 incident 指定类型的时间线条目数。
func countTimeline(t *testing.T, c *ent.Client, incID int, typ timelineitem.Type) int {
	t.Helper()
	n, err := c.TimelineItem.Query().
		Where(
			timelineitem.HasIncidentWith(incident.IDEQ(incID)),
			timelineitem.TypeEQ(typ),
		).Count(context.Background())
	if err != nil {
		t.Fatalf("count timeline: %v", err)
	}
	return n
}

// TestHandleResolved_WritesTimelineAndPublishesEvent 验证 B3：
// 自动恢复写 status_changed 时间线 + 发 IncidentResolved 领域事件。
func TestHandleResolved_WritesTimelineAndPublishesEvent(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng, captured := engineWithBusAndRecorder(t, c)
	ctx := context.Background()

	firing := createFiringEvent(t, c, "d1")
	res1, err := eng.Process(ctx, firing.ID)
	if err != nil {
		t.Fatalf("process firing: %v", err)
	}
	if res1.Action != ActionIncidentCreated {
		t.Fatalf("setup: firing action=%q", res1.Action)
	}
	incID := res1.IncidentID

	resolved := createResolvedEvent(t, c, "d1")
	res2, err := eng.Process(ctx, resolved.ID)
	if err != nil {
		t.Fatalf("process resolved: %v", err)
	}
	if res2.Action != ActionResolved {
		t.Fatalf("resolved action: got %q, want resolved", res2.Action)
	}
	if res2.IncidentID != incID {
		t.Fatalf("resolved wrong incident: got %d want %d", res2.IncidentID, incID)
	}

	if got := countTimeline(t, c, incID, timelineitem.TypeStatusChanged); got != 1 {
		t.Errorf("status_changed timeline count: got %d, want 1", got)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured IncidentResolved events: got %d, want 1", len(*captured))
	}
	ev := (*captured)[0]
	if ev.Incident == nil || ev.Incident.ID != incID {
		t.Errorf("event incident: got %+v, want id=%d", ev.Incident, incID)
	}
	if !ev.SystemTriggered {
		t.Error("auto-resolve event should be SystemTriggered=true")
	}
	if string(ev.Action) != "resolve" {
		t.Errorf("event action: got %q, want resolve", ev.Action)
	}
}

// TestHandleResolved_DoesNotResolveOtherIncident 验证 B3 收敛匹配：
// resolved 事件按 dedup 维度定位，不误解同 service 下 dedup 不同的其它活跃单。
func TestHandleResolved_DoesNotResolveOtherIncident(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng, _ := engineWithBusAndRecorder(t, c)
	ctx := context.Background()

	f1 := createFiringEvent(t, c, "d1")
	r1, _ := eng.Process(ctx, f1.ID)
	if r1.Action != ActionIncidentCreated {
		t.Fatalf("setup f1: %q", r1.Action)
	}
	f2, err := c.Event.Create().
		SetSourceEventID("d2").SetSource("prometheus").
		SetSeverity(event.SeverityCritical).SetStatus(event.StatusFiring).
		SetSummary("另一告警 d2").SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey("d2").Save(ctx)
	if err != nil {
		t.Fatalf("create f2: %v", err)
	}
	r2, _ := eng.Process(ctx, f2.ID)
	if r2.Action != ActionIncidentCreated {
		t.Fatalf("setup f2: %q", r2.Action)
	}

	resolved := createResolvedEvent(t, c, "d1")
	res, err := eng.Process(ctx, resolved.ID)
	if err != nil {
		t.Fatalf("process resolved: %v", err)
	}
	if res.IncidentID != r1.IncidentID {
		t.Errorf("resolved wrong incident: got %d, want %d (d1's)", res.IncidentID, r1.IncidentID)
	}

	inc2, _ := c.Incident.Get(ctx, r2.IncidentID)
	if inc2.Status == incident.StatusResolved {
		t.Errorf("d2 incident wrongly resolved: status=%q", inc2.Status)
	}
	inc1, _ := c.Incident.Get(ctx, r1.IncidentID)
	if inc1.Status != incident.StatusResolved {
		t.Errorf("d1 incident should be resolved: status=%q", inc1.Status)
	}
}

// TestHandleResolved_SkipsAckedIncident 验证 B3 acked 语义：
// 已 acked 的单（已有人接手）不被自动恢复替人关单。
func TestHandleResolved_SkipsAckedIncident(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng, captured := engineWithBusAndRecorder(t, c)
	ctx := context.Background()

	firing := createFiringEvent(t, c, "d1")
	r1, _ := eng.Process(ctx, firing.ID)
	if r1.Action != ActionIncidentCreated {
		t.Fatalf("setup: %q", r1.Action)
	}
	if err := c.Incident.UpdateOneID(r1.IncidentID).
		SetStatus(incident.StatusAcked).Exec(ctx); err != nil {
		t.Fatalf("ack incident: %v", err)
	}

	resolved := createResolvedEvent(t, c, "d1")
	res, err := eng.Process(ctx, resolved.ID)
	if err != nil {
		t.Fatalf("process resolved: %v", err)
	}
	if res.Action == ActionResolved {
		t.Errorf("acked incident should NOT be auto-resolved, got action=%q", res.Action)
	}

	inc, _ := c.Incident.Get(ctx, r1.IncidentID)
	if inc.Status != incident.StatusAcked {
		t.Errorf("acked incident status changed: got %q, want acked", inc.Status)
	}
	if len(*captured) != 0 {
		t.Errorf("should not publish resolve event for acked incident, got %d", len(*captured))
	}
}
