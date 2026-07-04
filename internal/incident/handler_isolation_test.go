// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// 验证核心安全断言：team 级权限的用户不能访问/操作其他 team 的资源。
// 这是 ARCH-02/SEC-01 改动价值的直接证明——修复前这些用例会全部"通过"（因为无隔离），
// 修复后必须正确返回 403/空列表。
package incident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// isoSetup 隔离测试场景：
//   - teamA（pay）+ incA 归属 teamA
//   - teamB（order）+ incB 归属 teamB
//   - userA：仅 teamA 的 incident.view 权限（team 级 binding）
//   - userB：仅 teamB 的 incident.view 权限
type isoData struct {
	c     *ent.Client
	teamA int
	teamB int
	incA  int
	incB  int
	userA int // teamA 权限
	userB int // teamB 权限
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:iso_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

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
		SetPermissions([]string{string(auth.PermIncidentView)}).
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
			SetTeamID(itoa(p.tid)).
			SetGrantedAt(time.Now()).
			Save(ctx); err != nil {
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
	return isoData{c: c, teamA: ta.ID, teamB: tb.ID, incA: incA.ID, incB: incB.ID, userA: ua.ID, userB: ub.ID}
}

// newIsolatedHandler 构造带真实 authz+scope 的 handler 与 echo group（身份解析用 X-Vigil-User-ID）。
func newIsolatedHandler(d isoData) (*echo.Echo, *Handler) {
	svc := NewService(d.c, timeline.NewRecorder(d.c), nil)
	h := NewHandler(d.c, svc)
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)
	return e, h
}

// getAsUser 以 uid 身份发请求。
func getAsUser(e *echo.Echo, path string, uid int) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Vigil-User-ID", itoa(uid))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestIsolation_GetOwnTeamIncident_OK userA 访问自己 team 的 incident → 200（基线）。
func TestIsolation_GetOwnTeamIncident_OK(t *testing.T) {
	d := isoSetup(t)
	e, _ := newIsolatedHandler(d)
	rec := getAsUser(e, "/api/v1/incidents/"+itoa(d.incA), d.userA)
	if rec.Code != http.StatusOK {
		t.Errorf("userA access own team incident: got %d, want 200 (baseline should pass)", rec.Code)
	}
}

// TestIsolation_GetOtherTeamIncident_403 userA（teamA）访问 teamB 的 incident → 403。
// ★ 核心断言：修复前这里会返回 200（越权），修复后必须 403。
func TestIsolation_GetOtherTeamIncident_403(t *testing.T) {
	d := isoSetup(t)
	e, _ := newIsolatedHandler(d)
	rec := getAsUser(e, "/api/v1/incidents/"+itoa(d.incB), d.userA)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA access teamB incident: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

// TestIsolation_GetOtherTeamIncident_Symmetric userB 访问 teamA 的 incident → 403（对称验证）。
func TestIsolation_GetOtherTeamIncident_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e, _ := newIsolatedHandler(d)
	rec := getAsUser(e, "/api/v1/incidents/"+itoa(d.incA), d.userB)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB access teamA incident: got %d, want 403", rec.Code)
	}
}

// TestIsolation_ListOnlyVisibleTeam userA 列表只见 teamA 的 incident（total=1）。
// ★ 核心断言：修复前 total=2（泄露 teamB 数据），修复后 total=1。
func TestIsolation_ListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e, _ := newIsolatedHandler(d)
	rec := getAsUser(e, "/api/v1/incidents", d.userA)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("userA should see only 1 incident (own team), got total=%d (cross-team leak!)", resp.Total)
	}
}

// TestIsolation_AckOtherTeamIncident_403 userA 操作 teamB 的 incident（写操作）→ 403。
// 注：ack 需要 incident.ack 权限，userA 只有 view，本应因缺权限拒绝。
// 但即便给 userA 加 ack 权限，仍应因跨 team 被 ScopeResolver 拦截。
func TestIsolation_AckOtherTeamIncident_403(t *testing.T) {
	d := isoSetup(t)
	// 给 userA 也加 ack 权限（同 team scope），验证跨 team 仍被拒
	ctx := context.Background()
	ackRole, err := d.c.Role.Create().
		SetName("responder").
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermIncidentAck), string(auth.PermIncidentView)}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create ack role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).
		SetRoleID(ackRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(itoa(d.teamA)). // 仅 teamA
		SetGrantedAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("create binding: %v", err)
	}

	e, _ := newIsolatedHandler(d)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(d.incB)+"/ack", nil)
	req.Header.Set("X-Vigil-User-ID", itoa(d.userA))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	// userA 有 ack 权限但只限 teamA；操作 teamB 的 incident 应 403
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA ack teamB incident: got %d, want 403 (cross-team write isolation FAILED)", rec.Code)
	}
	// ★ 回归断言：仅校验 403 会漏掉 checkAccess 短路失效——handler 写完 403 仍继续执行 Ack。
	// 必须回读 incB：状态须仍为 triggered（未被 ack），否则即"报 403 却已落库"的越权。
	incB, err := d.c.Incident.Get(ctx, d.incB)
	if err != nil {
		t.Fatalf("reload incB: %v", err)
	}
	if incB.Status != incident.StatusTriggered {
		t.Errorf("incB status mutated despite 403: got %s, want triggered (checkAccess short-circuit FAILED)", incB.Status)
	}
}

// TestIsolation_CrossTeamWrite_StateUnchanged userA（仅 teamA 全套写权限）对 teamB 的 incident
// 逐一发起 ack/resolve/escalate，每次都应 403 且 incB 状态保持 triggered 不变。
//
// ★ 本用例专治 checkAccess 短路失效：errs.Forbidden 写完 403 按 echo 惯例返回 nil，若 handler
// 未因哨兵中止就会继续执行写操作，形成"报 403 却已落库/已改状态"的越权。仅断言响应码无法暴露
// 该 bug（响应码确为 403），必须回读资源状态。
func TestIsolation_CrossTeamWrite_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	ctx := context.Background()
	// 给 userA 授予 teamA 范围的全套写权限（ack/resolve/escalate/reopen + view）。
	writerRole, err := d.c.Role.Create().
		SetName("writer").
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermIncidentView),
			string(auth.PermIncidentAck),
			string(auth.PermIncidentResolve),
			string(auth.PermIncidentEscalate),
			string(auth.PermIncidentReopen),
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create writer role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).
		SetRoleID(writerRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(itoa(d.teamA)). // 仅 teamA
		SetGrantedAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("create binding: %v", err)
	}

	e, _ := newIsolatedHandler(d)
	// incB 初始 triggered；ack/escalate 均为 triggered 的合法转移，若跨 team 鉴权被绕过就会真的改状态。
	for _, action := range []string{"ack", "resolve", "escalate"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(d.incB)+"/"+action, nil)
		req.Header.Set("X-Vigil-User-ID", itoa(d.userA))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("userA %s teamB incident: got %d, want 403", action, rec.Code)
		}
		incB, err := d.c.Incident.Get(ctx, d.incB)
		if err != nil {
			t.Fatalf("reload incB after %s: %v", action, err)
		}
		if incB.Status != incident.StatusTriggered {
			t.Errorf("incB status mutated by denied %s: got %s, want triggered (403-but-mutated bug)", action, incB.Status)
		}
	}
}

// TestIsolation_OrgWideUserSeesAll org 级权限用户应看到全部（不被隔离）。
func TestIsolation_OrgWideUserSeesAll(t *testing.T) {
	d := isoSetup(t)
	ctx := context.Background()
	orgRole, err := d.c.Role.Create().
		SetName("admin").
		SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermIncidentView)}).
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

	e, _ := newIsolatedHandler(d)
	rec := getAsUser(e, "/api/v1/incidents", d.userA)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d", rec.Code)
	}
	var resp struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("org-wide user should see all 2 incidents, got %d", resp.Total)
	}
}
