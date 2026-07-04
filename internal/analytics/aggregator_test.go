// aggregator_test.go T6.1 定时聚合快照测试：
//   - 聚合生成 org 全局 + 每团队快照
//   - 快照与实时聚合口径一致
//   - 幂等：重跑覆盖不产重复行
//   - 读快照路径按 scope 隔离（org / 单团队）
package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/metricssnapshot"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/team"
)

// seedYesterday 灌入落在「昨天」窗口内的数据（daily 快照聚合上一个完整窗口 = 昨天）：
//   - teamA：3 event（1 噪音）+ 2 incident（1 resolved）+ 1 published 复盘
//   - teamB：1 event + 1 incident
//
// 返回 teamA/teamB id。
func seedYesterday(t *testing.T, c *ent.Client) (int, int) {
	t.Helper()
	ctx := context.Background()
	// 昨天窗口内的时间点（daily 窗口 = 昨天 00:00 ~ 今天 00:00）。
	now := time.Now()
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := todayMidnight.Add(-6 * time.Hour) // 昨天 18:00，稳落窗口内

	ta, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	tb, _ := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	svcA, _ := c.Service.Create().SetName("checkout").SetSlug("chk").SetTeamID(ta.ID).Save(ctx)
	svcB, _ := c.Service.Create().SetName("ordersvc").SetSlug("ord").SetTeamID(tb.ID).Save(ctx)

	// teamA events：2 非噪音（绑 svcA）+ 1 噪音（未绑）。
	for i, noise := range []bool{false, false, true} {
		ec := c.Event.Create().
			SetSourceEventID("a" + itoaInt(i)).
			SetSource("prometheus").
			SetSeverity(event.SeverityCritical).
			SetStatus(event.StatusFiring).
			SetSummary("A" + itoaInt(i)).
			SetLabels(map[string]string{}).
			SetDedupKey("da" + itoaInt(i)).
			SetIsNoise(noise).
			SetReceivedAt(yesterday)
		if !noise {
			ec.SetService(svcA)
		}
		if _, err := ec.Save(ctx); err != nil {
			t.Fatalf("create teamA event: %v", err)
		}
	}
	// teamB event：1 非噪音（绑 svcB）。
	if _, err := c.Event.Create().
		SetSourceEventID("b0").SetSource("prometheus").
		SetSeverity(event.SeverityWarning).SetStatus(event.StatusFiring).
		SetSummary("B0").SetLabels(map[string]string{}).SetDedupKey("db0").
		SetIsNoise(false).SetReceivedAt(yesterday).SetService(svcB).Save(ctx); err != nil {
		t.Fatalf("create teamB event: %v", err)
	}

	// teamA incidents：1 critical resolved + 1 warning acked。
	_, _ = c.Incident.Create().
		SetNumber("INC-A1").SetTitle("a1").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusResolved).SetPriority(incident.PriorityP1).
		SetSummary("a1").SetTriggerType(incident.TriggerTypeAuto).SetTeamID(ta.ID).
		SetCreatedAt(yesterday.Add(-30 * time.Minute)).SetResolvedAt(yesterday).Save(ctx)
	_, _ = c.Incident.Create().
		SetNumber("INC-A2").SetTitle("a2").SetSeverity(incident.SeverityWarning).
		SetStatus(incident.StatusAcked).SetPriority(incident.PriorityP2).
		SetSummary("a2").SetTriggerType(incident.TriggerTypeAuto).SetTeamID(ta.ID).
		SetCreatedAt(yesterday.Add(-10 * time.Minute)).SetAckedAt(yesterday.Add(-5 * time.Minute)).Save(ctx)
	// teamB incident：1 critical。
	incB, _ := c.Incident.Create().
		SetNumber("INC-B1").SetTitle("b1").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).SetPriority(incident.PriorityP1).
		SetSummary("b1").SetTriggerType(incident.TriggerTypeAuto).SetTeamID(tb.ID).
		SetCreatedAt(yesterday.Add(-20 * time.Minute)).Save(ctx)
	_ = incB

	// teamA published 复盘（挂 INC-A1）。
	incA1, _ := c.Incident.Query().Where(incident.NumberEQ("INC-A1")).Only(ctx)
	_, _ = c.Postmortem.Create().
		SetIncidentID(incA1.ID).SetStatus(postmortem.StatusPublished).
		SetGeneratedBy(postmortem.GeneratedByHuman).SetSections(map[string]any{}).
		SetCreatedAt(yesterday).Save(ctx)

	return ta.ID, tb.ID
}

// TestAggregate_WritesOrgAndTeamSnapshots 聚合应写 org 全局 + 每团队各一份快照。
func TestAggregate_WritesOrgAndTeamSnapshots(t *testing.T) {
	c := newTestClient(t)
	teamA, teamB := seedYesterday(t, c)
	ctx := context.Background()
	s := NewSnapshotter(c)

	n, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	// org 全局 1 + teamA + teamB = 3 快照。
	if n != 3 {
		t.Fatalf("expected 3 snapshots, got %d", n)
	}

	// org 全局快照：team=nil，告警合计 total=4（3 teamA + 1 teamB）。
	orgSnap, err := c.MetricsSnapshot.Query().
		Where(metricssnapshot.Not(metricssnapshot.HasTeam())).Only(ctx)
	if err != nil {
		t.Fatalf("query org snapshot: %v", err)
	}
	if orgSnap.AlertsTotal != 4 {
		t.Errorf("org alerts_total: got %d, want 4", orgSnap.AlertsTotal)
	}
	if orgSnap.IncidentsTotal != 3 {
		t.Errorf("org incidents_total: got %d, want 3", orgSnap.IncidentsTotal)
	}

	// teamA 快照：2 event（team scope 只计绑 svcA 的 2 条；未路由噪音 event 无 service → 不属任何团队）
	// + 2 incident + 1 published 复盘。
	teamASnap, err := c.MetricsSnapshot.Query().
		Where(metricssnapshot.HasTeamWith(team.IDEQ(teamA))).Only(ctx)
	if err != nil {
		t.Fatalf("query teamA snapshot: %v", err)
	}
	if teamASnap.AlertsTotal != 2 || teamASnap.IncidentsTotal != 2 {
		t.Errorf("teamA snapshot: alerts=%d incidents=%d, want 2/2", teamASnap.AlertsTotal, teamASnap.IncidentsTotal)
	}
	if teamASnap.PostmortemsPublished != 1 {
		t.Errorf("teamA postmortems_published: got %d, want 1", teamASnap.PostmortemsPublished)
	}

	// teamB 快照：1 event / 1 incident / 0 复盘。
	teamBSnap, err := c.MetricsSnapshot.Query().
		Where(metricssnapshot.HasTeamWith(team.IDEQ(teamB))).Only(ctx)
	if err != nil {
		t.Fatalf("query teamB snapshot: %v", err)
	}
	if teamBSnap.AlertsTotal != 1 || teamBSnap.IncidentsTotal != 1 {
		t.Errorf("teamB snapshot: alerts=%d incidents=%d, want 1/1", teamBSnap.AlertsTotal, teamBSnap.IncidentsTotal)
	}
}

// TestAggregate_SnapshotMatchesRealtime 快照口径应与实时聚合一致（同窗口同 scope）。
func TestAggregate_SnapshotMatchesRealtime(t *testing.T) {
	c := newTestClient(t)
	teamA, _ := seedYesterday(t, c)
	ctx := context.Background()
	s := NewSnapshotter(c)

	if _, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily); err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	// 实时算同一窗口（昨天）的 teamA 告警度量。
	start, end := periodWindow(metricssnapshot.PeriodDaily, time.Now())
	scope := Scope{TeamIDs: []int{teamA}}
	realtime, err := s.engine.AlertMetrics(ctx, Range{Start: start, End: end}, scope)
	if err != nil {
		t.Fatalf("realtime AlertMetrics: %v", err)
	}
	fromSnap, err := s.LatestAlertFromSnapshot(ctx, scope, metricssnapshot.PeriodDaily)
	if err != nil {
		t.Fatalf("LatestAlertFromSnapshot: %v", err)
	}
	if fromSnap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if fromSnap.Total != realtime.Total || fromSnap.Notified != realtime.Notified ||
		fromSnap.Unrouted != realtime.Unrouted {
		t.Errorf("snapshot != realtime: snap=%+v realtime=%+v", fromSnap, realtime)
	}

	// incident 度量一致性（含 severity 分布）。
	rtInc, err := s.engine.IncidentMetrics(ctx, Range{Start: start, End: end}, scope)
	if err != nil {
		t.Fatalf("realtime IncidentMetrics: %v", err)
	}
	snapInc, err := s.LatestIncidentFromSnapshot(ctx, scope, metricssnapshot.PeriodDaily)
	if err != nil {
		t.Fatalf("LatestIncidentFromSnapshot: %v", err)
	}
	if snapInc == nil || snapInc.Total != rtInc.Total {
		t.Errorf("incident snapshot mismatch: snap=%+v realtime=%+v", snapInc, rtInc)
	}
	if snapInc.BySeverity["critical"] != rtInc.BySeverity["critical"] {
		t.Errorf("severity dist mismatch: snap=%v realtime=%v", snapInc.BySeverity, rtInc.BySeverity)
	}
}

// TestAggregate_Idempotent 重跑同窗口聚合应覆盖不产重复行。
func TestAggregate_Idempotent(t *testing.T) {
	c := newTestClient(t)
	seedYesterday(t, c)
	ctx := context.Background()
	s := NewSnapshotter(c)

	if _, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily); err != nil {
		t.Fatalf("first Aggregate: %v", err)
	}
	firstCount, _ := c.MetricsSnapshot.Query().Count(ctx)
	// 再跑一次。
	if _, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily); err != nil {
		t.Fatalf("second Aggregate: %v", err)
	}
	secondCount, _ := c.MetricsSnapshot.Query().Count(ctx)
	if firstCount != secondCount {
		t.Errorf("idempotency broken: %d snapshots after first, %d after second", firstCount, secondCount)
	}
	// org 全局仍恰好 1 行（NULL team 去重兜底生效）。
	orgN, _ := c.MetricsSnapshot.Query().Where(metricssnapshot.Not(metricssnapshot.HasTeam())).Count(ctx)
	if orgN != 1 {
		t.Errorf("expected 1 org snapshot after rerun, got %d", orgN)
	}
}

// TestLatestSnapshot_ScopeIsolation 读快照按 scope 隔离：org 读全局、单团队读该团队、多团队降级 nil。
func TestLatestSnapshot_ScopeIsolation(t *testing.T) {
	c := newTestClient(t)
	teamA, teamB := seedYesterday(t, c)
	ctx := context.Background()
	s := NewSnapshotter(c)
	if _, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily); err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	// org 全局：total=4。
	org, err := s.LatestAlertFromSnapshot(ctx, AllTeams(), metricssnapshot.PeriodDaily)
	if err != nil || org == nil {
		t.Fatalf("org snapshot: %v / nil=%v", err, org == nil)
	}
	if org.Total != 4 {
		t.Errorf("org total: got %d, want 4", org.Total)
	}

	// 单团队 A：total=2（team scope 只计绑 svcA 的路由 event，未路由噪音不计）；单团队 B：total=1。
	a, _ := s.LatestAlertFromSnapshot(ctx, Scope{TeamIDs: []int{teamA}}, metricssnapshot.PeriodDaily)
	if a == nil || a.Total != 2 {
		t.Errorf("teamA snapshot total: got %+v, want 2", a)
	}
	b, _ := s.LatestAlertFromSnapshot(ctx, Scope{TeamIDs: []int{teamB}}, metricssnapshot.PeriodDaily)
	if b == nil || b.Total != 1 {
		t.Errorf("teamB snapshot total: got %+v, want 1", b)
	}

	// 多团队 scope：快照按单团队分行，无法合并 → 返回 nil（调用方降级实时）。
	multi, err := s.LatestAlertFromSnapshot(ctx, Scope{TeamIDs: []int{teamA, teamB}}, metricssnapshot.PeriodDaily)
	if err != nil {
		t.Fatalf("multi-team snapshot: %v", err)
	}
	if multi != nil {
		t.Errorf("multi-team snapshot should be nil (fall back to realtime), got %+v", multi)
	}
}

// TestLatestSnapshot_NoSnapshot 无快照时返回 nil（调用方降级实时）。
func TestLatestSnapshot_NoSnapshot(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	s := NewSnapshotter(c)
	// 未聚合，直接读快照。
	got, err := s.LatestAlertFromSnapshot(ctx, AllTeams(), metricssnapshot.PeriodDaily)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (no snapshot), got %+v", got)
	}
}
