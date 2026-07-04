// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// AI 诊断/相似查询按 incident 归属 team 鉴权，AI 建议确认按 ai_insight→incident→team 反查：
// team 级权限用户不能触发/查看/确认其他 team 的 AI 能力。除断言 403 外，对 resolve（写）端点
// 回读 insight 状态，专治 checkAccess 短路失效（d98843a 修复类）——「报 403 却已落库」的越权。
package ai

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

type isoData struct {
	c            *ent.Client
	teamA, teamB int
	incA, incB   int
	insightA     int // 归属 teamA（经 incA）的 AI 建议（同 team 权限门控用）
	insightB     int // 归属 teamB（经 incB）的 AI 建议
	userA, userB int
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:ai_iso?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	ta, err := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create teamA: %v", err)
	}
	tb, err := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	if err != nil {
		t.Fatalf("create teamB: %v", err)
	}

	viewerRole, err := c.Role.Create().
		SetName("viewer").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermIncidentView)}).Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	ua, err := c.User.Create().SetUsername("alice").SetEmail("a@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create userA: %v", err)
	}
	ub, err := c.User.Create().SetUsername("bob").SetEmail("b@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create userB: %v", err)
	}
	for _, p := range []struct{ uid, tid int }{{ua.ID, ta.ID}, {ub.ID, tb.ID}} {
		if _, err := c.RoleBinding.Create().
			SetUserID(p.uid).SetRoleID(viewerRole.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).
			SetTeamID(strconv.Itoa(p.tid)).SetGrantedAt(time.Now()).Save(ctx); err != nil {
			t.Fatalf("create binding: %v", err)
		}
	}

	incA, err := c.Incident.Create().
		SetNumber("INC-A").SetTitle("A").SetSeverity(incident.SeverityWarning).
		SetStatus(incident.StatusTriggered).SetTeamID(ta.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incA: %v", err)
	}
	incB, err := c.Incident.Create().
		SetNumber("INC-B").SetTitle("B").SetSeverity(incident.SeverityWarning).
		SetStatus(incident.StatusTriggered).SetTeamID(tb.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incB: %v", err)
	}

	insB, err := c.AIInsight.Create().
		SetStage(aiinsight.StageDiagnose).
		SetType(aiinsight.TypeRootCauseHint).
		SetContent(map[string]any{"root_cause": "x"}).
		SetIncidentID(incB.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create insightB: %v", err)
	}
	insA, err := c.AIInsight.Create().
		SetStage(aiinsight.StageDiagnose).
		SetType(aiinsight.TypeRootCauseHint).
		SetContent(map[string]any{"root_cause": "y"}).
		SetIncidentID(incA.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create insightA: %v", err)
	}

	return isoData{c: c, teamA: ta.ID, teamB: tb.ID, incA: incA.ID, incB: incB.ID, insightA: insA.ID, insightB: insB.ID, userA: ua.ID, userB: ub.ID}
}

func newIsolatedHandler(d isoData) *echo.Echo {
	h := NewHandler(NewDiagnoseEngine(d.c, nil))
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true)))
	h.Register(v1)
	return e
}

func reqAsUser(e *echo.Echo, method, path string, uid int, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestIsolation_DiagnoseOwnTeam_OK userA 触发自己 team 事件的诊断 → 非 403（基线；
// provider 为 nil 时返回 200 disabled，重点是不被隔离拒绝）。
func TestIsolation_DiagnoseOwnTeam_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.incA)+"/diagnose", d.userA, "")
	if rec.Code == http.StatusForbidden {
		t.Errorf("userA diagnose own incident: got 403, want non-403 (baseline should not be isolated)")
	}
}

// TestIsolation_DiagnoseOtherTeam_403 userA 触发 teamB 事件的诊断 → 403。
func TestIsolation_DiagnoseOtherTeam_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/diagnose", d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA diagnose teamB incident: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

// TestIsolation_SimilarOtherTeam_403 userA 查 teamB 事件的相似历史 → 403。
func TestIsolation_SimilarOtherTeam_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/similar", d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA similar teamB incident: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

// TestIsolation_DiagnoseOtherTeam_Symmetric userB 触发 teamA 事件的诊断 → 403（对称）。
func TestIsolation_DiagnoseOtherTeam_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.incA)+"/diagnose", d.userB, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB diagnose teamA incident: got %d, want 403", rec.Code)
	}
}

// TestIsolation_CrossTeamResolveInsight_StateUnchanged userA 确认 teamB 的 AI 建议 → 403 且
// insight 状态不变。★ resolve 是写端点：仅断言 403 会漏掉 checkAccess 短路失效，必须回读状态。
func TestIsolation_CrossTeamResolveInsight_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/ai-insights/"+strconv.Itoa(d.insightB)+"/resolve", d.userA, `{"accepted":true}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA resolve teamB insight: got %d, want 403 (cross-team write isolation FAILED)", rec.Code)
	}
	ins, err := d.c.AIInsight.Get(t.Context(), d.insightB)
	if err != nil {
		t.Fatalf("reload insightB: %v", err)
	}
	if ins.Status != aiinsight.StatusSuggested {
		t.Errorf("insightB status mutated despite 403: got %s, want suggested (403-but-persisted bug)", ins.Status)
	}
}

// grantTeamPerm 给 uid 在 tid 团队额外绑定一个含指定权限点的角色（同 team 权限门控测试用）。
func grantTeamPerm(t *testing.T, c *ent.Client, uid, tid int, perm auth.Permission) {
	t.Helper()
	ctx := t.Context()
	r, err := c.Role.Create().
		SetName("resolver_" + strconv.Itoa(uid)).SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermIncidentView), string(perm)}).Save(ctx)
	if err != nil {
		t.Fatalf("create resolver role: %v", err)
	}
	if _, err := c.RoleBinding.Create().
		SetUserID(uid).SetRoleID(r.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(strconv.Itoa(tid)).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("bind resolver role: %v", err)
	}
}

// TestResolve_SubscriberEquivalent_403 只读角色（仅 incident.view，同 subscriber）改判本 team 的
// AI 建议 → 403，且状态不变。S11：resolve 须 ai.insight.resolve，不能挂只读 incident.view。
func TestResolve_SubscriberEquivalent_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	// userA 在 teamA 只有 viewer 角色（incident.view），无 ai.insight.resolve。
	rec := reqAsUser(e, http.MethodPost, "/api/v1/ai-insights/"+strconv.Itoa(d.insightA)+"/resolve", d.userA, `{"accepted":true}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer resolve own-team insight: got %d, want 403 (处置级权限缺失应拒绝)", rec.Code)
	}
	ins, err := d.c.AIInsight.Get(t.Context(), d.insightA)
	if err != nil {
		t.Fatalf("reload insightA: %v", err)
	}
	if ins.Status != aiinsight.StatusSuggested {
		t.Errorf("insightA status mutated despite 403: got %s, want suggested", ins.Status)
	}
}

// TestResolve_WithPerm_OK 持 ai.insight.resolve 的用户改判本 team 建议 → 非 403 且状态改判成功。
func TestResolve_WithPerm_OK(t *testing.T) {
	d := isoSetup(t)
	grantTeamPerm(t, d.c, d.userA, d.teamA, auth.PermAIInsightResolve)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/ai-insights/"+strconv.Itoa(d.insightA)+"/resolve", d.userA, `{"accepted":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized resolve: got %d, want 200", rec.Code)
	}
	ins, err := d.c.AIInsight.Get(t.Context(), d.insightA)
	if err != nil {
		t.Fatalf("reload insightA: %v", err)
	}
	if ins.Status != aiinsight.StatusAccepted {
		t.Errorf("after authorized accept: got %s, want accepted", ins.Status)
	}
}

// TestIsolation_OrgWideUserSeesAll org 级用户不被隔离。
func TestIsolation_OrgWideUserSeesAll(t *testing.T) {
	d := isoSetup(t)
	ctx := t.Context()
	orgRole, err := d.c.Role.Create().
		SetName("admin").SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermIncidentView)}).Save(ctx)
	if err != nil {
		t.Fatalf("create org role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).SetRoleID(orgRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create org binding: %v", err)
	}
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/diagnose", d.userA, "")
	if rec.Code == http.StatusForbidden {
		t.Errorf("org-wide userA should not be isolated from teamB diagnose, got 403")
	}
}
