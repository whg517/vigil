package analytics

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:ana_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedData 灌入：3 个 event（1 噪音，2 个绑 service 1 个不绑）+ 2 个 incident（1 resolved 1 acked）+ 1 复盘。
func seedData(t *testing.T, c *ent.Client) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	// team + service
	team, _ := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	svc, _ := c.Service.Create().
		SetName("payment-api").SetSlug("payment").SetTeamID(team.ID).Save(ctx)

	// events: 2 非噪音（绑 service）+ 1 噪音（不绑 service，模拟 unrouted）
	for i, noise := range []bool{false, false, true} {
		ec := c.Event.Create().
			SetSourceEventID("e" + itoa(i)).
			SetSource("prometheus").
			SetSeverity(event.SeverityCritical).
			SetStatus(event.StatusFiring).
			SetSummary("告警" + itoa(i)).
			SetLabels(map[string]string{"service": "payment"}).
			SetDedupKey("d" + itoa(i)).
			SetIsNoise(noise).
			SetReceivedAt(now)
		if !noise {
			ec.SetService(svc) // 非噪音的命中路由；噪音的 unrouted
		}
		if _, err := ec.Save(ctx); err != nil {
			t.Fatalf("create event: %v", err)
		}
	}

	// incidents: 1 critical resolved + 1 warning acked（带 acked_at 验证 MTTA）
	ackTime := now.Add(-5 * time.Minute)
	_, _ = c.Incident.Create().
		SetNumber("INC-1").SetTitle("a").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusResolved).SetPriority(incident.PriorityP1).
		SetSummary("a").SetTriggerType(incident.TriggerTypeAuto).
		SetTeamID(team.ID).
		SetCreatedAt(now.Add(-30 * time.Minute)).
		SetResolvedAt(now).
		Save(ctx)
	_, _ = c.Incident.Create().
		SetNumber("INC-2").SetTitle("b").SetSeverity(incident.SeverityWarning).
		SetStatus(incident.StatusAcked).SetPriority(incident.PriorityP2).
		SetSummary("b").SetTriggerType(incident.TriggerTypeAuto).
		SetTeamID(team.ID).
		SetCreatedAt(now.Add(-10 * time.Minute)).
		SetAckedAt(ackTime).
		Save(ctx)

	// postmortem: 1 published，挂在第一个 incident
	incs, _ := c.Incident.Query().All(ctx)
	if len(incs) > 0 {
		_, _ = c.Postmortem.Create().
			SetIncidentID(incs[0].ID).
			SetStatus(postmortem.StatusPublished).
			SetGeneratedBy(postmortem.GeneratedByHuman).
			SetSections(map[string]any{}).
			Save(ctx)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func TestAlertMetrics(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	m, err := eng.AlertMetrics(context.Background(), Range{})
	if err != nil {
		t.Fatalf("AlertMetrics: %v", err)
	}
	if m.Total != 3 {
		t.Errorf("Total: got %d, want 3", m.Total)
	}
	if m.Notified != 2 { // 2 非噪音
		t.Errorf("Notified: got %d, want 2", m.Notified)
	}
	// 降噪率 = 1 - 2/3 ≈ 0.333
	if m.NoiseRate < 0.3 || m.NoiseRate > 0.4 {
		t.Errorf("NoiseRate: got %f, want ~0.33", m.NoiseRate)
	}
	// unrouted = 未命中 service 的 event（噪音那条未绑 service）
	if m.Unrouted != 1 {
		t.Errorf("Unrouted: got %d, want 1", m.Unrouted)
	}
}

func TestIncidentMetrics(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	m, err := eng.IncidentMetrics(context.Background(), Range{})
	if err != nil {
		t.Fatalf("IncidentMetrics: %v", err)
	}
	if m.Total != 2 {
		t.Errorf("Total: got %d, want 2", m.Total)
	}
	if m.BySeverity["critical"] != 1 || m.BySeverity["warning"] != 1 {
		t.Errorf("BySeverity: got %+v", m.BySeverity)
	}
	if m.ResolvedCount != 1 {
		t.Errorf("ResolvedCount: got %d, want 1", m.ResolvedCount)
	}
	// MTTR ≈ 1800 秒（30 分钟）
	if m.MTTRatio < 1700 || m.MTTRatio > 1900 {
		t.Errorf("MTTRatio: got %f, want ~1800", m.MTTRatio)
	}
	// MTTA = INC-2 的 acked_at(-5min) - created_at(-10min) ≈ 300 秒（5 分钟）
	if m.MTTARatio < 290 || m.MTTARatio > 310 {
		t.Errorf("MTTARatio: got %f, want ~300", m.MTTARatio)
	}
}

func TestTeamLoad(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	load, err := eng.TeamLoad(context.Background(), Range{})
	if err != nil {
		t.Fatalf("TeamLoad: %v", err)
	}
	if len(load) != 1 {
		t.Fatalf("expected 1 team, got %d", len(load))
	}
	if load[0].Incidents != 2 {
		t.Errorf("team incidents: got %d, want 2", load[0].Incidents)
	}
}

func TestPostmortemMetrics(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	m, err := eng.PostmortemMetrics(context.Background(), Range{})
	if err != nil {
		t.Fatalf("PostmortemMetrics: %v", err)
	}
	if m.Total != 1 || m.Published != 1 {
		t.Errorf("got total=%d published=%d, want 1/1", m.Total, m.Published)
	}
	if m.CompletionRate != 1.0 {
		t.Errorf("CompletionRate: got %f, want 1.0", m.CompletionRate)
	}
}

func TestTrend(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	points, err := eng.Trend(context.Background(), 7, Range{})
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if len(points) != 7 {
		t.Fatalf("expected 7 points, got %d", len(points))
	}
	// 今天应有数据（2 incident + 3 event）
	today := points[len(points)-1]
	if today.Incidents != 2 {
		t.Errorf("today incidents: got %d, want 2", today.Incidents)
	}
	if today.Events != 3 {
		t.Errorf("today events: got %d, want 3", today.Events)
	}
}

func TestDashboard(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	d, err := eng.Dashboard(context.Background(), 7)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if d.Alert == nil || d.Incident == nil || d.Postmortem == nil {
		t.Error("dashboard sections should not be nil")
	}
	if d.Incident.Total != 2 {
		t.Errorf("dashboard incident total: got %d", d.Incident.Total)
	}
}
