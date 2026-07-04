// notifier_test.go T4.4 订阅定向通知引擎测试：
//   - 订阅 team 后该 team 的 Incident 事件触发定向通知
//   - 订阅 service 后该 service 的 Incident 事件触发定向通知
//   - 同时订阅 team+service 只发一次（按订阅者去重）
//   - min_severity 过滤：低于阈值不通知
//   - 禁用订阅者不通知
package subscription

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	entincident "github.com/kevin/vigil/ent/incident"
	entsubscription "github.com/kevin/vigil/ent/subscription"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/notification"

	_ "github.com/mattn/go-sqlite3"
)

// stubNotifier 记录被定向通知到的 user id + 严重度，供断言。
type stubNotifier struct {
	calls []stubCall
}

type stubCall struct {
	incidentID int
	userIDs    []int
}

func (s *stubNotifier) NotifyTargeted(_ context.Context, inc *ent.Incident, targets []notification.Target, _ []string) error {
	uids := make([]int, 0, len(targets))
	for _, t := range targets {
		uids = append(uids, t.UserID)
	}
	s.calls = append(s.calls, stubCall{incidentID: inc.ID, userIDs: uids})
	return nil
}

// notifiedUsers 汇总所有被通知到的 user id（跨多次调用）。
func (s *stubNotifier) notifiedUsers() map[int]int {
	out := map[int]int{}
	for _, c := range s.calls {
		for _, u := range c.userIDs {
			out[u]++
		}
	}
	return out
}

func newClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttestOpen(t)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkUser 建一个用户（默认 active）。
func mkUser(t *testing.T, c *ent.Client, name string) *ent.User {
	t.Helper()
	u, err := c.User.Create().
		SetUsername(name).SetEmail(name + "@x.io").SetName(name).
		Save(context.Background())
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	return u
}

// mkIncidentWithTeamService 建带 team+service 的 incident，返回 (inc, team, service)。
func mkIncidentWithTeamService(t *testing.T, c *ent.Client, severity string) (*ent.Incident, *ent.Team, *ent.Service) {
	t.Helper()
	ctx := context.Background()
	tm, err := c.Team.Create().SetName("pay").SetSlug("pay-" + t.Name()).Save(ctx)
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	svc, err := c.Service.Create().SetName("checkout").SetSlug("chk-" + t.Name()).SetTeamID(tm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-" + t.Name()).
		SetTitle("boom").
		SetSeverity(entincident.Severity(severity)).
		SetStatus("triggered").
		SetPriority("p1").
		SetTriggerType("auto").
		SetTeamID(tm.ID).
		SetService(svc).
		Save(ctx)
	if err != nil {
		t.Fatalf("incident: %v", err)
	}
	return inc, tm, svc
}

// TestOnIncidentEvent_TeamSubscriber 订阅 team → 该 team 的 Incident 事件触发定向通知。
func TestOnIncidentEvent_TeamSubscriber(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	inc, tm, _ := mkIncidentWithTeamService(t, c, "critical")
	leader := mkUser(t, c, "leader")

	_, err := c.Subscription.Create().
		SetUserID(leader.ID).SetTeamID(tm.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).
		Save(ctx)
	if err != nil {
		t.Fatalf("subscription: %v", err)
	}

	sn := &stubNotifier{}
	n := NewNotifier(c, sn)
	if err := n.OnIncidentEvent(ctx, event.Event{Type: event.IncidentCreated, Incident: inc}); err != nil {
		t.Fatalf("OnIncidentEvent: %v", err)
	}
	if got := sn.notifiedUsers()[leader.ID]; got != 1 {
		t.Errorf("team subscriber should be notified once, got %d", got)
	}
}

// TestOnIncidentEvent_ServiceSubscriber 订阅 service → 该 service 的 Incident 事件触发定向通知。
func TestOnIncidentEvent_ServiceSubscriber(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	inc, _, svc := mkIncidentWithTeamService(t, c, "warning")
	biz := mkUser(t, c, "bizowner")

	_, err := c.Subscription.Create().
		SetUserID(biz.ID).SetServiceID(svc.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).
		Save(ctx)
	if err != nil {
		t.Fatalf("subscription: %v", err)
	}

	sn := &stubNotifier{}
	n := NewNotifier(c, sn)
	if err := n.OnIncidentEvent(ctx, event.Event{Type: event.IncidentResolved, Incident: inc}); err != nil {
		t.Fatalf("OnIncidentEvent: %v", err)
	}
	if got := sn.notifiedUsers()[biz.ID]; got != 1 {
		t.Errorf("service subscriber should be notified once, got %d", got)
	}
}

// TestOnIncidentEvent_DedupTeamAndService 同一人同时订 team 与 service → 只发一次（去重）。
func TestOnIncidentEvent_DedupTeamAndService(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	inc, tm, svc := mkIncidentWithTeamService(t, c, "critical")
	u := mkUser(t, c, "both")

	_, _ = c.Subscription.Create().SetUserID(u.ID).SetTeamID(tm.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).Save(ctx)
	_, _ = c.Subscription.Create().SetUserID(u.ID).SetServiceID(svc.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).Save(ctx)

	sn := &stubNotifier{}
	n := NewNotifier(c, sn)
	if err := n.OnIncidentEvent(ctx, event.Event{Type: event.IncidentCreated, Incident: inc}); err != nil {
		t.Fatalf("OnIncidentEvent: %v", err)
	}
	if got := sn.notifiedUsers()[u.ID]; got != 1 {
		t.Errorf("subscriber of both team+service should be notified once (dedup), got %d", got)
	}
}

// TestOnIncidentEvent_MinSeverityFilter Incident 严重度低于订阅阈值 → 不通知。
func TestOnIncidentEvent_MinSeverityFilter(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	inc, tm, _ := mkIncidentWithTeamService(t, c, "info") // info 事件
	u := mkUser(t, c, "warnonly")

	// 订阅阈值 warning：info 事件低于阈值，不应通知。
	_, _ = c.Subscription.Create().SetUserID(u.ID).SetTeamID(tm.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).Save(ctx)

	sn := &stubNotifier{}
	n := NewNotifier(c, sn)
	if err := n.OnIncidentEvent(ctx, event.Event{Type: event.IncidentCreated, Incident: inc}); err != nil {
		t.Fatalf("OnIncidentEvent: %v", err)
	}
	if got := sn.notifiedUsers()[u.ID]; got != 0 {
		t.Errorf("info incident should not notify a warning-min subscriber, got %d", got)
	}
}

// TestOnIncidentEvent_DisabledSubscriberSkipped 禁用订阅者不通知。
func TestOnIncidentEvent_DisabledSubscriberSkipped(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	inc, tm, _ := mkIncidentWithTeamService(t, c, "critical")
	u := mkUser(t, c, "gone")
	// 禁用该用户。
	_, _ = c.User.UpdateOneID(u.ID).SetStatus("disabled").Save(ctx)

	_, _ = c.Subscription.Create().SetUserID(u.ID).SetTeamID(tm.ID).
		SetMinSeverity(entsubscription.MinSeverityWarning).Save(ctx)

	sn := &stubNotifier{}
	n := NewNotifier(c, sn)
	if err := n.OnIncidentEvent(ctx, event.Event{Type: event.IncidentCreated, Incident: inc}); err != nil {
		t.Fatalf("OnIncidentEvent: %v", err)
	}
	if got := sn.notifiedUsers()[u.ID]; got != 0 {
		t.Errorf("disabled subscriber should not be notified, got %d", got)
	}
}
