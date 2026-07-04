// export_test.go T6.1 CSV 导出测试：
//   - 导出返回 CSV（Content-Type + Content-Disposition 附件头）
//   - 导出内容格式正确（表头 + 数据行）
//   - 导出遵循 team scope 隔离（team 级用户只见本团队数据）
//   - source=snapshot 读快照 / 无快照降级实时
package analytics

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/metricssnapshot"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
)

// exportEnv 导出测试场景：teamA/teamB 各有数据；
//   - orgUser：org 级角色（看全组织）
//   - teamAUser：仅 teamA 可见（team scope）
type exportEnv struct {
	c            *ent.Client
	teamA, teamB int
	orgUser      int
	teamAUser    int
}

func exportSetup(t *testing.T) exportEnv {
	t.Helper()
	c := newTestClient(t)
	ctx := context.Background()
	now := time.Now()

	ta, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	tb, _ := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	svcA, _ := c.Service.Create().SetName("checkout").SetSlug("chk").SetTeamID(ta.ID).Save(ctx)
	svcB, _ := c.Service.Create().SetName("ordersvc").SetSlug("ord").SetTeamID(tb.ID).Save(ctx)

	// teamA：2 event（绑 svcA）+ 2 incident；teamB：1 event（绑 svcB）+ 1 incident。
	for i := 0; i < 2; i++ {
		_, _ = c.Event.Create().SetSourceEventID("a" + itoaInt(i)).SetSource("p").
			SetSeverity(event.SeverityCritical).SetStatus(event.StatusFiring).
			SetSummary("A").SetLabels(map[string]string{}).SetDedupKey("da" + itoaInt(i)).
			SetIsNoise(false).SetReceivedAt(now).SetService(svcA).Save(ctx)
	}
	_, _ = c.Event.Create().SetSourceEventID("b0").SetSource("p").
		SetSeverity(event.SeverityWarning).SetStatus(event.StatusFiring).
		SetSummary("B").SetLabels(map[string]string{}).SetDedupKey("db0").
		SetIsNoise(false).SetReceivedAt(now).SetService(svcB).Save(ctx)

	for i := 0; i < 2; i++ {
		_, _ = c.Incident.Create().SetNumber("INC-A" + itoaInt(i)).SetTitle("a").
			SetSeverity(incident.SeverityCritical).SetStatus(incident.StatusTriggered).
			SetPriority(incident.PriorityP1).SetSummary("a").
			SetTriggerType(incident.TriggerTypeAuto).SetTeamID(ta.ID).SetCreatedAt(now).Save(ctx)
	}
	_, _ = c.Incident.Create().SetNumber("INC-B0").SetTitle("b").
		SetSeverity(incident.SeverityWarning).SetStatus(incident.StatusTriggered).
		SetPriority(incident.PriorityP2).SetSummary("b").
		SetTriggerType(incident.TriggerTypeAuto).SetTeamID(tb.ID).SetCreatedAt(now).Save(ctx)

	// org 级角色（org scope binding → orgWide）+ team 级角色（仅 teamA）。
	orgRole, _ := c.Role.Create().SetName("org_admin").SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermAnalyticsView)}).Save(ctx)
	teamRole, _ := c.Role.Create().SetName("team_lead").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermAnalyticsView)}).Save(ctx)

	orgU, _ := c.User.Create().SetUsername("root").SetEmail("root@x.io").Save(ctx)
	teamAU, _ := c.User.Create().SetUsername("alead").SetEmail("alead@x.io").Save(ctx)

	_, _ = c.RoleBinding.Create().SetUserID(orgU.ID).SetRoleID(orgRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(now).Save(ctx)
	_, _ = c.RoleBinding.Create().SetUserID(teamAU.ID).SetRoleID(teamRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(ta.ID)).
		SetGrantedAt(now).Save(ctx)

	return exportEnv{c: c, teamA: ta.ID, teamB: tb.ID, orgUser: orgU.ID, teamAUser: teamAU.ID}
}

func exportEcho(env exportEnv) *echo.Echo {
	h := NewHandler(NewEngine(env.c)).
		SetAuthorizer(auth.NewAuthorizer(env.c)).
		SetSnapshotter(NewSnapshotter(env.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)
	return e
}

func doReq(e *echo.Echo, path string, uid int) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// parseCSV 解析响应体为记录（含表头）。
func parseCSV(t *testing.T, body string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v (body=%q)", err, body)
	}
	return recs
}

// TestExportAlerts_CSVHeadersAndBody 导出返回 CSV 附件头 + 正确内容。
func TestExportAlerts_CSVHeadersAndBody(t *testing.T) {
	env := exportSetup(t)
	e := exportEcho(env)

	rec := doReq(e, "/api/v1/analytics/alerts/export", env.orgUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get(echo.HeaderContentType)
	if !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type: got %q, want text/csv", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "alerts_") {
		t.Errorf("Content-Disposition: got %q, want attachment with alerts_ filename", cd)
	}
	recs := parseCSV(t, rec.Body.String())
	if len(recs) < 2 || recs[0][0] != "metric" || recs[0][1] != "value" {
		t.Fatalf("unexpected CSV header: %v", recs[0])
	}
	// org 用户看全组织：total=3（2 teamA + 1 teamB）。
	got := csvValue(recs, "total")
	if got != "3" {
		t.Errorf("alerts total (org): got %q, want 3", got)
	}
}

// TestExportIncidents_TeamScope teamA 用户导出只见本团队事件（2），不含 teamB。
func TestExportIncidents_TeamScope(t *testing.T) {
	env := exportSetup(t)
	e := exportEcho(env)

	// org 用户：total=3（含 teamA+teamB）。
	orgRec := doReq(e, "/api/v1/analytics/incidents/export", env.orgUser)
	if orgRec.Code != http.StatusOK {
		t.Fatalf("org export: %d %s", orgRec.Code, orgRec.Body.String())
	}
	if v := csvValue(parseCSV(t, orgRec.Body.String()), "total"); v != "3" {
		t.Errorf("org incidents total: got %q, want 3", v)
	}

	// teamA 用户：只见 teamA 的 2 个，不含 teamB。
	teamRec := doReq(e, "/api/v1/analytics/incidents/export", env.teamAUser)
	if teamRec.Code != http.StatusOK {
		t.Fatalf("team export: %d %s", teamRec.Code, teamRec.Body.String())
	}
	if v := csvValue(parseCSV(t, teamRec.Body.String()), "total"); v != "2" {
		t.Errorf("teamA incidents total: got %q, want 2 (team scope isolation)", v)
	}
}

// TestExportTeamLoad_TeamScope teamA 用户导出团队负载只见 teamA 一行。
func TestExportTeamLoad_TeamScope(t *testing.T) {
	env := exportSetup(t)
	e := exportEcho(env)

	rec := doReq(e, "/api/v1/analytics/team-load/export", env.teamAUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("team-load export: %d %s", rec.Code, rec.Body.String())
	}
	recs := parseCSV(t, rec.Body.String())
	// 表头 + 恰好 1 行（teamA）。
	if len(recs) != 2 {
		t.Fatalf("teamA team-load rows: got %d records, want 2 (header + teamA)", len(recs))
	}
	if recs[1][2] != "2" { // teamA 有 2 个 incident
		t.Errorf("teamA incidents in CSV: got %q, want 2", recs[1][2])
	}
}

// TestExportPostmortems_OK 复盘度量导出返回 CSV。
func TestExportPostmortems_OK(t *testing.T) {
	env := exportSetup(t)
	e := exportEcho(env)
	rec := doReq(e, "/api/v1/analytics/postmortems/export", env.orgUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("postmortems export: %d %s", rec.Code, rec.Body.String())
	}
	recs := parseCSV(t, rec.Body.String())
	if csvValue(recs, "total") != "0" { // 场景未建复盘
		t.Errorf("postmortems total: got %q, want 0", csvValue(recs, "total"))
	}
}

// TestSourceSnapshot_ReadsSnapshotThenFallsBack source=snapshot 有快照读快照、无快照降级实时。
func TestSourceSnapshot_ReadsSnapshotThenFallsBack(t *testing.T) {
	env := exportSetup(t)
	e := exportEcho(env)
	ctx := context.Background()

	// 未聚合前：source=snapshot 无快照 → 降级实时（org total=3）。
	rec := doReq(e, "/api/v1/analytics/alerts?source=snapshot", env.orgUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot(no data): %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total":3`) {
		t.Errorf("fallback realtime expected total 3, got %s", rec.Body.String())
	}

	// 写一条 org 全局快照（total=99，人为区别于实时），验证 source=snapshot 命中快照。
	_, err := env.c.MetricsSnapshot.Create().
		SetPeriod(metricssnapshot.PeriodDaily).
		SetPeriodStart(time.Now().Add(-24 * time.Hour)).
		SetPeriodEnd(time.Now()).
		SetAlertsTotal(99).Save(ctx)
	if err != nil {
		t.Fatalf("seed org snapshot: %v", err)
	}
	rec2 := doReq(e, "/api/v1/analytics/alerts?source=snapshot", env.orgUser)
	if !strings.Contains(rec2.Body.String(), `"total":99`) {
		t.Errorf("source=snapshot should read snapshot total 99, got %s", rec2.Body.String())
	}
	// 默认（无 source）仍读实时 total=3。
	rec3 := doReq(e, "/api/v1/analytics/alerts", env.orgUser)
	if !strings.Contains(rec3.Body.String(), `"total":3`) {
		t.Errorf("default realtime total 3, got %s", rec3.Body.String())
	}
}

// csvValue 在 [metric,value] 两列 CSV 中按 metric 名取 value（未找到返回空串）。
func csvValue(recs [][]string, metric string) string {
	for _, r := range recs {
		if len(r) >= 2 && r[0] == metric {
			return r[1]
		}
	}
	return ""
}
