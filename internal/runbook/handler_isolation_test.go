// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// 验证核心安全断言：team 级权限的用户不能访问/操作/执行其他 team 的 Runbook。
// Runbook execute 触发真实副作用（对外 HTTP 写动作），是 CLAUDE.md「Runbook 写操作须
// require_approval」边界的最敏感入口，故本文件除断言 403 外，还对写/执行端点回读资源状态，
// 专治 checkAccess 短路失效（d98843a 修复类）——「报 403 却已落库/已执行」的越权。
package runbook

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// isoData 隔离测试场景：
//   - teamA + rbA 归属 teamA；teamB + rbB 归属 teamB
//   - userA：仅 teamA 的 runbook.view 权限（team 级 binding）
//   - userB：仅 teamB 的 runbook.view 权限
type isoData struct {
	c     *ent.Client
	teamA int
	teamB int
	rbA   int
	rbB   int
	userA int
	userB int
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:runbook_iso?mode=memory&cache=shared&_fk=1")
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
		SetName("viewer").
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermRunbookView)}).
		Save(ctx)
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
			SetUserID(p.uid).
			SetRoleID(viewerRole.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).
			SetTeamID(strconv.Itoa(p.tid)).
			SetGrantedAt(time.Now()).
			Save(ctx); err != nil {
			t.Fatalf("create binding: %v", err)
		}
	}

	rbA, err := c.Runbook.Create().
		SetName("rb-a").SetType(entrunbook.TypeDocument).SetTeamID(ta.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create rbA: %v", err)
	}
	rbB, err := c.Runbook.Create().
		SetName("rb-b").SetType(entrunbook.TypeDocument).SetTeamID(tb.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create rbB: %v", err)
	}
	return isoData{c: c, teamA: ta.ID, teamB: tb.ID, rbA: rbA.ID, rbB: rbB.ID, userA: ua.ID, userB: ub.ID}
}

// grantWriter 给 uid 授予 teamID 范围的全套写/执行权限，验证跨 team 仍被拦。
func grantWriter(t *testing.T, d isoData, uid, teamID int) {
	t.Helper()
	ctx := t.Context()
	wr, err := d.c.Role.Create().
		SetName("writer-" + strconv.Itoa(teamID) + "-" + strconv.Itoa(uid)).
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermRunbookView),
			string(auth.PermRunbookUpdate),
			string(auth.PermRunbookDelete),
			string(auth.PermRunbookExecute),
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create writer role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(uid).
		SetRoleID(wr.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(strconv.Itoa(teamID)).
		SetGrantedAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("create writer binding: %v", err)
	}
}

// newIsolatedHandler 构造带真实 authz+scope 的 handler 与 echo group（身份解析用 X-Vigil-User-ID）。
func newIsolatedHandler(d isoData) *echo.Echo {
	h := NewHandler(d.c, NewEngine(d.c, newTestRegistry()))
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)
	return e
}

func reqAsUser(e *echo.Echo, method, path string, uid int, body string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestIsolation_GetOwnTeamRunbook_OK userA 访问自己 team 的 runbook → 200（基线）。
func TestIsolation_GetOwnTeamRunbook_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/runbooks/"+strconv.Itoa(d.rbA), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("userA get own runbook: got %d, want 200 (baseline)", rec.Code)
	}
}

// TestIsolation_GetOtherTeamRunbook_403 userA（teamA）访问 teamB 的 runbook → 403。
func TestIsolation_GetOtherTeamRunbook_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/runbooks/"+strconv.Itoa(d.rbB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA get teamB runbook: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

// TestIsolation_GetOtherTeamRunbook_Symmetric userB 访问 teamA 的 runbook → 403（对称）。
func TestIsolation_GetOtherTeamRunbook_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/runbooks/"+strconv.Itoa(d.rbA), d.userB, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB get teamA runbook: got %d, want 403", rec.Code)
	}
}

// TestIsolation_ListOnlyVisibleTeam userA 列表只见 teamA 的 runbook。
func TestIsolation_ListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/runbooks", d.userA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, body=%s", rec.Code, rec.Body.String())
	}
	// list 返回裸数组 []*ent.Runbook，只应含 teamA 的 rbA。
	body := rec.Body.String()
	if strings.Contains(body, `"rb-b"`) || !strings.Contains(body, `"rb-a"`) {
		t.Errorf("userA list should contain only rb-a (own team), got: %s (cross-team leak!)", body)
	}
}

// TestIsolation_CrossTeamUpdate_StateUnchanged userA（teamA 全套写权限）PATCH teamB 的 runbook
// → 403 且 rbB 名称保持不变。★ 仅断言 403 会漏掉 checkAccess 短路失效，必须回读资源状态。
func TestIsolation_CrossTeamUpdate_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA) // 仅 teamA
	e := newIsolatedHandler(d)

	rec := reqAsUser(e, http.MethodPatch, "/api/v1/runbooks/"+strconv.Itoa(d.rbB), d.userA, `{"name":"hacked"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA patch teamB runbook: got %d, want 403", rec.Code)
	}
	rbB, err := d.c.Runbook.Get(t.Context(), d.rbB)
	if err != nil {
		t.Fatalf("reload rbB: %v", err)
	}
	if rbB.Name != "rb-b" {
		t.Errorf("rbB name mutated despite 403: got %q, want rb-b (checkAccess short-circuit FAILED)", rbB.Name)
	}
}

// TestIsolation_CrossTeamDelete_StateUnchanged userA DELETE teamB 的 runbook → 403 且 rbB 仍在。
func TestIsolation_CrossTeamDelete_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)

	rec := reqAsUser(e, http.MethodDelete, "/api/v1/runbooks/"+strconv.Itoa(d.rbB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA delete teamB runbook: got %d, want 403", rec.Code)
	}
	if exist, err := d.c.Runbook.Query().Where(entrunbook.IDEQ(d.rbB)).Exist(t.Context()); err != nil {
		t.Fatalf("check rbB exist: %v", err)
	} else if !exist {
		t.Error("rbB deleted despite 403 (checkAccess short-circuit FAILED)")
	}
}

// TestIsolation_CrossTeamExecute_NoSideEffect userA 触发 teamB 的可执行 runbook → 403 且
// 底层步骤（对外 HTTP）绝不被调用。★ execute 是最敏感的写入口：只读诊断步骤即便 approved=false
// 也会执行并打到目标端点，故用 httptest 计数器作 canary——若 checkAccess 未短路，execute 会真正
// 运行并命中 canary（「报 403 却已执行」）。
func TestIsolation_CrossTeamExecute_NoSideEffect(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA) // 含 runbook.execute，但仅 teamA

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1) // canary 被打 = execute 越权执行
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"logs":"ok"}`))
	}))
	defer srv.Close()

	// 归属 teamB 的可执行 runbook，含一个只读诊断步骤（无需 approval 即会执行）。
	rbExec, err := d.c.Runbook.Create().
		SetName("rb-exec-b").
		SetType(entrunbook.TypeExecutable).
		SetTeamID(d.teamB).
		SetSteps([]schema.RunbookStep{{
			ID: "s1", Name: "查日志",
			Action:    schema.StepAction{Type: "diagnose", Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: true}},
			OnFailure: "continue",
		}}).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create exec runbook: %v", err)
	}

	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/runbooks/"+strconv.Itoa(rbExec.ID)+"/execute", d.userA, `{"incident_id":0,"approved":false}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA execute teamB runbook: got %d, want 403 (cross-team execute isolation FAILED)", rec.Code)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("teamB runbook step executed despite 403: canary hit %d times (403-but-executed bug)", n)
	}
}

// TestIsolation_OrgWideUserSeesAll org 级权限用户应看到全部（不被隔离）。
func TestIsolation_OrgWideUserSeesAll(t *testing.T) {
	d := isoSetup(t)
	ctx := t.Context()
	orgRole, err := d.c.Role.Create().
		SetName("admin").
		SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermRunbookView)}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create org role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).
		SetRoleID(orgRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).
		SetGrantedAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("create org binding: %v", err)
	}

	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/runbooks/"+strconv.Itoa(d.rbB), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("org-wide userA should access teamB runbook, got %d", rec.Code)
	}
}
