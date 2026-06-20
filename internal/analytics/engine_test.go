package analytics

import (
	"context"
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

// seedData 灌入：3 个 event（1 噪音）+ 2 个 incident（1 resolved）+ 1 复盘（published）。
func seedData(t *testing.T, c *ent.Client) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	// team + service
	team, _ := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)

	// events: 2 非噪音 + 1 噪音
	for i, noise := range []bool{false, false, true} {
		_, err := c.Event.Create().
			SetSourceEventID("e"+itoa(i)).
			SetSource("prometheus").
			SetSeverity(event.SeverityCritical).
			SetStatus(event.StatusFiring).
			SetSummary("告警" + itoa(i)).
			SetLabels(map[string]string{"service": "payment"}).
			SetDedupKey("d" + itoa(i)).
			SetIsNoise(noise).
			SetReceivedAt(now).
			Save(ctx)
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
	}

	// incidents: 1 critical resolved + 1 warning acked
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
	return string(rune('0' + i))
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
