package incident

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
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

// TestAck_FromTriggered triggered → acked，assignee 设置，回调触发。
func TestAck_FromTriggered(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	user, err := c.User.Create().SetUsername("zhangsan").SetEmail("zs@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := timeline.NewRecorder(c)
	svc := NewService(c, rec, nil)

	var changedAction Action
	svc.SetOnIncidentChanged(func(_ context.Context, _ *ent.Incident, a Action) {
		changedAction = a
	})

	updated, err := svc.Ack(context.Background(), inc.ID, user.ID, SourceIM)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if updated.Status != incident.StatusAcked {
		t.Errorf("status: got %s, want acked", updated.Status)
	}
	if changedAction != ActionAck {
		t.Errorf("callback action: got %s, want %s", changedAction, ActionAck)
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
