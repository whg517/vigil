package incident

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// newClient sqlite 内存库 + 自动迁移。
func newClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:incident_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedIncident 建一个 Team + Incident（默认 triggered）。
func seedIncident(t *testing.T, c *ent.Client, status incident.Status) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	team, err := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("支付服务 5xx").
		SetSeverity(incident.SeverityCritical).
		SetStatus(status).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// TestAck_FromTriggered triggered → acked，assignee 设置，事件发布。
func TestAck_FromTriggered(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	user, err := c.User.Create().SetUsername("zhangsan").SetEmail("zs@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := timeline.NewRecorder(c)
	bus := event.New()
	svc := NewService(c, rec, bus)

	// 订阅 IncidentAcked，断言事件被发布（替代旧的 onIncidentChanged 回调）。
	var gotAction Action
	var gotActorID int
	bus.Subscribe(event.IncidentAcked, func(_ context.Context, e event.Event) error {
		gotAction = Action(e.Action)
		gotActorID = e.ActorID
		return nil
	})

	updated, err := svc.Ack(context.Background(), inc.ID, user.ID, SourceIM)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if updated.Status != incident.StatusAcked {
		t.Errorf("status: got %s, want acked", updated.Status)
	}
	if gotAction != ActionAck {
		t.Errorf("event action: got %s, want %s", gotAction, ActionAck)
	}
	if gotActorID != user.ID {
		t.Errorf("event actor id: got %d, want %d", gotActorID, user.ID)
	}
	// assignee 应设置
	a, _ := updated.QueryAssignee().Only(context.Background())
	if a == nil || a.ID != user.ID {
		t.Errorf("assignee not set to %d", user.ID)
	}
}

// TestAck_FromResolved 已 resolved 再 ack 应失败（状态机守卫）。
func TestAck_FromResolved(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusResolved)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Ack(context.Background(), inc.ID, 1, SourceWeb)
	if err == nil {
		t.Fatal("expected ErrInvalidTransition, got nil")
	}
}

// TestResolve_FromAcked acked → resolved，resolved_at 设置。
func TestResolve_FromAcked(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusAcked)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	updated, err := svc.Resolve(context.Background(), inc.ID, 1, SourceWeb)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if updated.Status != incident.StatusResolved {
		t.Errorf("status: got %s, want resolved", updated.Status)
	}
	if updated.ResolvedAt == nil {
		t.Error("resolved_at not set")
	}
}

// TestResolve_Twice 已 resolved 再 resolve 失败。
func TestResolve_Twice(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusResolved)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Resolve(context.Background(), inc.ID, 1, SourceWeb)
	if err == nil {
		t.Fatal("expected ErrInvalidTransition, got nil")
	}
}

// TestClose_FromResolved resolved → closed，写 status_changed 时间线，发 IncidentClosed 事件。
func TestClose_FromResolved(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusResolved)
	user, err := c.User.Create().SetUsername("closer").SetEmail("closer@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := timeline.NewRecorder(c)
	bus := event.New()
	svc := NewService(c, rec, bus)

	// 订阅 IncidentClosed，断言事件被发布。
	var gotAction Action
	var gotActorID int
	var gotStatus incident.Status
	bus.Subscribe(event.IncidentClosed, func(_ context.Context, e event.Event) error {
		gotAction = Action(e.Action)
		gotActorID = e.ActorID
		if e.Incident != nil {
			gotStatus = incident.Status(e.Incident.Status)
		}
		return nil
	})

	updated, err := svc.Close(context.Background(), inc.ID, user.ID, SourceWeb)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if updated.Status != incident.StatusClosed {
		t.Errorf("status: got %s, want closed", updated.Status)
	}
	if gotAction != ActionClose {
		t.Errorf("event action: got %s, want %s", gotAction, ActionClose)
	}
	if gotActorID != user.ID {
		t.Errorf("event actor id: got %d, want %d", gotActorID, user.ID)
	}
	if gotStatus != incident.StatusClosed {
		t.Errorf("event incident status: got %s, want closed", gotStatus)
	}
	// 应写一条 status_changed 时间线，detail.status=closed
	items, err := rec.Query(context.Background(), inc.ID, timelineitem.TypeStatusChanged, "", 10, 0)
	if err != nil {
		t.Fatalf("query timeline: %v", err)
	}
	var foundClosed bool
	for _, it := range items {
		if s, _ := it.Detail["status"].(string); s == "closed" {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Error("no status_changed timeline item with detail.status=closed")
	}
}

// TestClose_FromTriggered 非 resolved 状态（triggered）直接 close 应被状态机拒绝。
func TestClose_FromTriggered(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Close(context.Background(), inc.ID, 1, SourceWeb)
	if err == nil {
		t.Fatal("expected ErrInvalidTransition, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
	// 状态不应被改动
	got, _ := c.Incident.Get(context.Background(), inc.ID)
	if got.Status != incident.StatusTriggered {
		t.Errorf("status changed on invalid close: got %s, want triggered", got.Status)
	}
}

// TestClose_FromAcked acked 直接 close（跳过 resolved）也应被拒绝。
func TestClose_FromAcked(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusAcked)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Close(context.Background(), inc.ID, 1, SourceWeb)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// TestClose_Idempotent 已 closed 再 close 返回 ErrAlreadyClosed（幂等哨兵），非普通非法转换。
func TestClose_Idempotent(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusClosed)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Close(context.Background(), inc.ID, 1, SourceWeb)
	if !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("expected ErrAlreadyClosed, got %v", err)
	}
}

// TestClose_NotFound 不存在的 incident 返回 ErrNotFound。
func TestClose_NotFound(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), nil)
	_, err := svc.Close(context.Background(), 9999, 1, SourceWeb)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestReopen_FromClosed closed 可 reopen 回 triggered（验证终态非死路：终态 + reopen 边）。
func TestReopen_FromClosed(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusClosed)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	updated, err := svc.Reopen(context.Background(), inc.ID, 1, SourceWeb)
	if err != nil {
		t.Fatalf("Reopen from closed: %v", err)
	}
	if updated.Status != incident.StatusTriggered {
		t.Errorf("status: got %s, want triggered", updated.Status)
	}
}

// TestEscalate_AdvancesLevel 手动升级推进 current_level。
func TestEscalate_AdvancesLevel(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	updated, err := svc.Escalate(context.Background(), inc.ID, 1, SourceIM)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if updated.Status != incident.StatusEscalated {
		t.Errorf("status: got %s, want escalated", updated.Status)
	}
	if updated.CurrentLevel != inc.CurrentLevel+1 {
		t.Errorf("current_level: got %d, want %d", updated.CurrentLevel, inc.CurrentLevel+1)
	}
}

// TestAddResponder responder 加入并去重。
func TestAddResponder(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	target, err := c.User.Create().SetUsername("lisi").SetEmail("lisi@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	svc := NewService(c, timeline.NewRecorder(c), nil)

	if _, err := svc.AddResponder(context.Background(), inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	rs, _ := c.Incident.Query().Where(incident.IDEQ(inc.ID)).QueryResponders().All(context.Background())
	if len(rs) != 1 || rs[0].ID != target.ID {
		t.Errorf("responder not added, got %v", rs)
	}
}

// TestAck_NotFound 不存在的 incident 返回 ErrNotFound。
func TestAck_NotFound(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), nil)
	_, err := svc.Ack(context.Background(), 9999, 1, SourceWeb)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestRecord_TimelineWritten ack 后时间线有 TypeAck 记录。
func TestRecord_TimelineWritten(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	user, err := c.User.Create().SetUsername("u1").SetEmail("u1@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := timeline.NewRecorder(c)
	svc := NewService(c, rec, nil)

	if _, err := svc.Ack(context.Background(), inc.ID, user.ID, SourceIM); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	items, err := rec.Query(context.Background(), inc.ID, "", "", 10, 0)
	if err != nil {
		t.Fatalf("query timeline: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no timeline items recorded")
	}
	// IM 来源 ack 应记 source=im
	found := false
	for _, it := range items {
		if string(it.Type) == "ack" && string(it.Source) == "im" {
			found = true
		}
	}
	if !found {
		t.Error("no im ack timeline item found")
	}
}

// TestEscalate_FromResolved 已 resolved 再手动升级应被状态机拒绝。
func TestEscalate_FromResolved(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusResolved)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Escalate(context.Background(), inc.ID, 1, SourceWeb)
	if err == nil {
		t.Fatal("expected ErrInvalidTransition, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// TestEscalate_TimelineRecordsCorrectLevel 手动升级后时间线记录正确的 level，
// 且 level 来自更新后的 incident（修复前变量作用域 bug 会导致记录旧值）。
func TestEscalate_TimelineRecordsCorrectLevel(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	// 预设 current_level=2，升级后应为 3
	if err := c.Incident.UpdateOneID(inc.ID).SetCurrentLevel(2).Exec(context.Background()); err != nil {
		t.Fatalf("preset current_level: %v", err)
	}
	rec := timeline.NewRecorder(c)
	svc := NewService(c, rec, nil) // 无 escEngine，仅记时间线

	updated, err := svc.Escalate(context.Background(), inc.ID, 1, SourceWeb)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if updated.CurrentLevel != 3 {
		t.Fatalf("current_level: got %d, want 3", updated.CurrentLevel)
	}

	// 时间线应记录 level=3（更新后的值），而非 2（旧值）
	items, err := rec.Query(context.Background(), inc.ID, "escalated", "", 10, 0)
	if err != nil {
		t.Fatalf("query timeline: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no escalated timeline item")
	}
	// 至少有一条 detail.level=3
	var foundLevel3 bool
	for _, it := range items {
		if lv, ok := it.Detail["level"]; ok && lv != nil {
			if levelNum, ok2 := toInt(lv); ok2 && levelNum == 3 {
				foundLevel3 = true
			}
		}
	}
	if !foundLevel3 {
		t.Error("no timeline item with level=3 (updated value); 旧作用域 bug 残留?")
	}
}

// TestEscalate_NoPolicyWithoutEscEngine 无策略 + 无 escEngine 时不报错，仅记时间线。
func TestEscalate_NoPolicyWithoutEscEngine(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	svc := NewService(c, timeline.NewRecorder(c), nil) // 无策略无引擎

	updated, err := svc.Escalate(context.Background(), inc.ID, 0, SourceWeb)
	if err != nil {
		t.Fatalf("Escalate without policy/engine: %v", err)
	}
	if updated.Status != incident.StatusEscalated {
		t.Errorf("status: got %s, want escalated", updated.Status)
	}
}

// TestReopen_FromResolved_PublishesEvent resolved → triggered，
// 且发布 IncidentReopened 事件（escalation 据此重启升级链的前置条件）。
func TestReopen_FromResolved_PublishesEvent(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusResolved)
	bus := event.New()
	svc := NewService(c, timeline.NewRecorder(c), bus)

	var captured []event.Event
	bus.Subscribe(event.IncidentReopened, func(_ context.Context, e event.Event) error {
		captured = append(captured, e)
		return nil
	})

	updated, err := svc.Reopen(context.Background(), inc.ID, 7, SourceWeb)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if updated.Status != incident.StatusTriggered {
		t.Errorf("status: got %s, want triggered", updated.Status)
	}
	if updated.ResolvedAt != nil {
		t.Error("resolved_at should be cleared on reopen")
	}
	if len(captured) != 1 {
		t.Fatalf("IncidentReopened events: got %d, want 1", len(captured))
	}
	ev := captured[0]
	if ev.Incident == nil || ev.Incident.ID != inc.ID {
		t.Errorf("event incident: got %+v, want id=%d", ev.Incident, inc.ID)
	}
	// 事件载荷携带最新状态（triggered），escalation 订阅方据此重取并重启升级链。
	if ev.Incident.Status != incident.StatusTriggered {
		t.Errorf("event incident status: got %q, want triggered", ev.Incident.Status)
	}
	if string(ev.Action) != string(ActionReopen) {
		t.Errorf("event action: got %q, want %q", ev.Action, ActionReopen)
	}
	if ev.ActorID != 7 {
		t.Errorf("event actor id: got %d, want 7", ev.ActorID)
	}
}

// TestReopen_FromTriggered_Rejected 活跃态（triggered）reopen 无意义，应被状态机拒绝。
func TestReopen_FromTriggered_Rejected(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	svc := NewService(c, timeline.NewRecorder(c), nil)

	_, err := svc.Reopen(context.Background(), inc.ID, 1, SourceWeb)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// toInt 把 interface{} 转 int（json detail 里数字可能是 float64）。
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
