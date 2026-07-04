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

// mustEvent 建一条 event（可选绑 service），失败即 fail。用于 scope/unrouted 用例精确控制数据。
func mustEvent(t *testing.T, c *ent.Client, id string, at time.Time, noise bool, svc *ent.Service) {
	t.Helper()
	ec := c.Event.Create().
		SetSourceEventID(id).
		SetSource("prometheus").
		SetSeverity(event.SeverityCritical).
		SetStatus(event.StatusFiring).
		SetSummary(id).
		SetLabels(map[string]string{}).
		SetDedupKey(id).
		SetIsNoise(noise).
		SetReceivedAt(at)
	if svc != nil {
		ec.SetService(svc)
	}
	if _, err := ec.Save(context.Background()); err != nil {
		t.Fatalf("create event %s: %v", id, err)
	}
}

func TestAlertMetrics(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	m, err := eng.AlertMetrics(context.Background(), Range{}, AllTeams())
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
	// unrouted = 未命中 service 且非噪音（C25）。seedData 的唯一未绑 service 的 event
	// 本身就是噪音（is_noise=true），应被排除 → unrouted=0（噪音不算未路由）。
	if m.Unrouted != 0 {
		t.Errorf("Unrouted: got %d, want 0 (噪音不计入未路由)", m.Unrouted)
	}
}

// TestAlertMetrics_UnroutedExcludesNoise 正向覆盖 C25：
// 一条「未绑 service 且非噪音」的 event 才算真正 unrouted；噪音不计入。
func TestAlertMetrics_UnroutedExcludesNoise(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	now := time.Now()

	// e-routed：绑 service（不影响 unrouted）
	team, _ := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s").SetTeamID(team.ID).Save(ctx)
	mustEvent(t, c, "e-routed", now, false, svc)
	// e-unrouted：未绑 service 且非噪音 → 真正未路由，计入
	mustEvent(t, c, "e-unrouted", now, false, nil)
	// e-noise：未绑 service 但为噪音 → 已降噪，不计入未路由
	mustEvent(t, c, "e-noise", now, true, nil)

	eng := NewEngine(c)
	m, err := eng.AlertMetrics(ctx, Range{}, AllTeams())
	if err != nil {
		t.Fatalf("AlertMetrics: %v", err)
	}
	if m.Total != 3 {
		t.Errorf("Total: got %d, want 3", m.Total)
	}
	// 仅 e-unrouted 计入：未绑 service + 非噪音
	if m.Unrouted != 1 {
		t.Errorf("Unrouted: got %d, want 1 (仅非噪音的未路由 event)", m.Unrouted)
	}
}

func TestIncidentMetrics(t *testing.T) {
	c := newTestClient(t)
	seedData(t, c)
	eng := NewEngine(c)

	m, err := eng.IncidentMetrics(context.Background(), Range{}, AllTeams())
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

	load, err := eng.TeamLoad(context.Background(), Range{}, AllTeams())
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

	m, err := eng.PostmortemMetrics(context.Background(), Range{}, AllTeams())
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

	points, err := eng.Trend(context.Background(), 7, Range{}, AllTeams())
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

	d, err := eng.Dashboard(context.Background(), 7, AllTeams())
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

// twoTeamFixture 灌两个团队各自的数据，返回 (teamA.ID, teamB.ID)。
// A：2 event（1 未路由非噪音）+ 2 incident + 1 复盘；B：1 event + 1 incident + 1 复盘。
func twoTeamFixture(t *testing.T, c *ent.Client) (int, int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	teamA, _ := c.Team.Create().SetName("A").SetSlug("a").Save(ctx)
	teamB, _ := c.Team.Create().SetName("B").SetSlug("b").Save(ctx)
	svcA, _ := c.Service.Create().SetName("sa").SetSlug("sa").SetTeamID(teamA.ID).Save(ctx)
	svcB, _ := c.Service.Create().SetName("sb").SetSlug("sb").SetTeamID(teamB.ID).Save(ctx)

	// A：1 绑 A 的 service + 1 未绑（未路由非噪音，不归属任何 team）
	mustEvent(t, c, "a-routed", now, false, svcA)
	mustEvent(t, c, "a-unrouted", now, false, nil)
	// B：1 绑 B 的 service
	mustEvent(t, c, "b-routed", now, false, svcB)

	mkInc := func(num string, teamID int) int {
		inc, err := c.Incident.Create().
			SetNumber(num).SetTitle(num).SetSeverity(incident.SeverityCritical).
			SetStatus(incident.StatusResolved).SetPriority(incident.PriorityP1).
			SetSummary(num).SetTriggerType(incident.TriggerTypeAuto).
			SetTeamID(teamID).SetCreatedAt(now).SetResolvedAt(now).Save(ctx)
		if err != nil {
			t.Fatalf("create incident %s: %v", num, err)
		}
		return inc.ID
	}
	incA1 := mkInc("A-1", teamA.ID)
	mkInc("A-2", teamA.ID)
	incB1 := mkInc("B-1", teamB.ID)

	mkPM := func(incID int) {
		if _, err := c.Postmortem.Create().
			SetIncidentID(incID).SetStatus(postmortem.StatusPublished).
			SetGeneratedBy(postmortem.GeneratedByHuman).SetSections(map[string]any{}).Save(ctx); err != nil {
			t.Fatalf("create postmortem: %v", err)
		}
	}
	mkPM(incA1)
	mkPM(incB1)

	return teamA.ID, teamB.ID
}

// TestScope_TeamIsolation team 级视角只见本团队数据，跨团队数据不出现。
func TestScope_TeamIsolation(t *testing.T) {
	c := newTestClient(t)
	teamA, teamB := twoTeamFixture(t, c)
	eng := NewEngine(c)
	ctx := context.Background()
	scopeA := Scope{TeamIDs: []int{teamA}}

	// 事件度量：A 仅 2 incident（A-1/A-2），不含 B-1
	inc, err := eng.IncidentMetrics(ctx, Range{}, scopeA)
	if err != nil {
		t.Fatalf("IncidentMetrics: %v", err)
	}
	if inc.Total != 2 {
		t.Errorf("teamA incident total: got %d, want 2", inc.Total)
	}

	// 告警度量：A 仅 1 event 归属（a-routed 经 svcA）。未路由的 a-unrouted 不属任何 team，
	// team scope 下不计入 → Total=1、Unrouted=0。
	al, err := eng.AlertMetrics(ctx, Range{}, scopeA)
	if err != nil {
		t.Fatalf("AlertMetrics: %v", err)
	}
	if al.Total != 1 {
		t.Errorf("teamA alert total: got %d, want 1 (仅绑本团队 service 的 event)", al.Total)
	}
	if al.Unrouted != 0 {
		t.Errorf("teamA unrouted: got %d, want 0 (未路由 event 不归属 team)", al.Unrouted)
	}

	// 复盘度量：A 仅 1（挂 A-1）
	pm, err := eng.PostmortemMetrics(ctx, Range{}, scopeA)
	if err != nil {
		t.Fatalf("PostmortemMetrics: %v", err)
	}
	if pm.Total != 1 {
		t.Errorf("teamA postmortem total: got %d, want 1", pm.Total)
	}

	// 团队负载：A 视角只列 team A 一行，不出现 B
	load, err := eng.TeamLoad(ctx, Range{}, scopeA)
	if err != nil {
		t.Fatalf("TeamLoad: %v", err)
	}
	if len(load) != 1 || load[0].TeamID != teamA {
		t.Fatalf("teamA load should list only team A, got %+v", load)
	}
	if load[0].Incidents != 2 {
		t.Errorf("teamA load incidents: got %d, want 2", load[0].Incidents)
	}

	// 反向确认：B 视角看到的是 B 的数据（1 incident），与 A 隔离
	incB, err := eng.IncidentMetrics(ctx, Range{}, Scope{TeamIDs: []int{teamB}})
	if err != nil {
		t.Fatalf("IncidentMetrics B: %v", err)
	}
	if incB.Total != 1 {
		t.Errorf("teamB incident total: got %d, want 1", incB.Total)
	}
}

// TestScope_OrgWide org 级视角（org_admin）看全组织：A+B 全部计入。
func TestScope_OrgWide(t *testing.T) {
	c := newTestClient(t)
	twoTeamFixture(t, c)
	eng := NewEngine(c)
	ctx := context.Background()

	inc, err := eng.IncidentMetrics(ctx, Range{}, AllTeams())
	if err != nil {
		t.Fatalf("IncidentMetrics: %v", err)
	}
	if inc.Total != 3 { // A-1 A-2 B-1
		t.Errorf("org-wide incident total: got %d, want 3", inc.Total)
	}

	al, err := eng.AlertMetrics(ctx, Range{}, AllTeams())
	if err != nil {
		t.Fatalf("AlertMetrics: %v", err)
	}
	if al.Total != 3 { // a-routed a-unrouted b-routed
		t.Errorf("org-wide alert total: got %d, want 3", al.Total)
	}
	// a-unrouted 未绑 service 且非噪音 → 全组织口径下计 1 未路由
	if al.Unrouted != 1 {
		t.Errorf("org-wide unrouted: got %d, want 1", al.Unrouted)
	}

	load, err := eng.TeamLoad(ctx, Range{}, AllTeams())
	if err != nil {
		t.Fatalf("TeamLoad: %v", err)
	}
	if len(load) != 2 {
		t.Errorf("org-wide load should list both teams, got %d", len(load))
	}
}

// TestScope_NoVisibleTeam 无可见 team（非 org 级且列表空）→ 各指标为空、不误聚合全组织。
func TestScope_NoVisibleTeam(t *testing.T) {
	c := newTestClient(t)
	twoTeamFixture(t, c)
	eng := NewEngine(c)
	ctx := context.Background()
	none := Scope{} // OrgWide=false, TeamIDs=nil

	inc, err := eng.IncidentMetrics(ctx, Range{}, none)
	if err != nil {
		t.Fatalf("IncidentMetrics: %v", err)
	}
	if inc.Total != 0 {
		t.Errorf("no-visible-team incident total: got %d, want 0", inc.Total)
	}
	al, err := eng.AlertMetrics(ctx, Range{}, none)
	if err != nil {
		t.Fatalf("AlertMetrics: %v", err)
	}
	if al.Total != 0 {
		t.Errorf("no-visible-team alert total: got %d, want 0", al.Total)
	}
	load, err := eng.TeamLoad(ctx, Range{}, none)
	if err != nil {
		t.Fatalf("TeamLoad: %v", err)
	}
	if len(load) != 0 {
		t.Errorf("no-visible-team load: got %d rows, want 0", len(load))
	}
	// Trend 仍返回连续日期骨架但计数为 0
	points, err := eng.Trend(ctx, 7, Range{}, none)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if len(points) != 7 {
		t.Fatalf("trend points: got %d, want 7", len(points))
	}
	for _, p := range points {
		if p.Incidents != 0 || p.Events != 0 {
			t.Errorf("no-visible-team trend should be all zero, got %+v", p)
		}
	}
}
