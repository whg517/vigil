// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// team 级权限用户不能查看/改写/删除其他 team 的 Integration。除断言 403 外，对写端点回读
// 资源状态，专治 checkAccess 短路失效（d98843a 修复类）——「报 403 却已落库」的越权。
package integration

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	entintegration "github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

type isoData struct {
	c            *ent.Client
	teamA, teamB int
	intA, intB   int
	userA, userB int
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:integration_iso?mode=memory&cache=shared&_fk=1")
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
		SetPermissions([]string{string(auth.PermIntegrationView)}).Save(ctx)
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

	intA, err := c.Integration.Create().
		SetName("int-a").SetType(entintegration.TypeWebhook).SetToken("tk-a").SetTeamID(ta.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create intA: %v", err)
	}
	intB, err := c.Integration.Create().
		SetName("int-b").SetType(entintegration.TypeWebhook).SetToken("tk-b").SetTeamID(tb.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create intB: %v", err)
	}
	return isoData{c: c, teamA: ta.ID, teamB: tb.ID, intA: intA.ID, intB: intB.ID, userA: ua.ID, userB: ub.ID}
}

func grantWriter(t *testing.T, d isoData, uid, teamID int) {
	t.Helper()
	ctx := t.Context()
	wr, err := d.c.Role.Create().
		SetName("writer-" + strconv.Itoa(teamID) + "-" + strconv.Itoa(uid)).
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermIntegrationView), string(auth.PermIntegrationUpdate), string(auth.PermIntegrationDelete),
		}).Save(ctx)
	if err != nil {
		t.Fatalf("create writer role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(uid).SetRoleID(wr.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(strconv.Itoa(teamID)).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create writer binding: %v", err)
	}
}

func newIsolatedHandler(d isoData) *echo.Echo {
	h := NewHandler(d.c)
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
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

func TestIsolation_GetOwnTeamIntegration_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/integrations/"+strconv.Itoa(d.intA), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("userA get own integration: got %d, want 200 (baseline)", rec.Code)
	}
}

func TestIsolation_GetOtherTeamIntegration_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/integrations/"+strconv.Itoa(d.intB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA get teamB integration: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

func TestIsolation_GetOtherTeamIntegration_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/integrations/"+strconv.Itoa(d.intA), d.userB, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB get teamA integration: got %d, want 403", rec.Code)
	}
}

func TestIsolation_IntegrationListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/integrations", d.userA, "")
	body := rec.Body.String()
	if strings.Contains(body, `"int-b"`) || !strings.Contains(body, `"int-a"`) {
		t.Errorf("userA integration list should contain only int-a, got: %s (cross-team leak!)", body)
	}
}

// TestIsolation_CrossTeamUpdateIntegration_StateUnchanged ★ 回读校验短路失效。
func TestIsolation_CrossTeamUpdateIntegration_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPatch, "/api/v1/integrations/"+strconv.Itoa(d.intB), d.userA, `{"name":"hacked","enabled":false}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA patch teamB integration: got %d, want 403", rec.Code)
	}
	integ, err := d.c.Integration.Get(t.Context(), d.intB)
	if err != nil {
		t.Fatalf("reload intB: %v", err)
	}
	if integ.Name != "int-b" || !integ.Enabled {
		t.Errorf("intB mutated despite 403: got name=%q enabled=%v, want int-b/true (short-circuit FAILED)", integ.Name, integ.Enabled)
	}
}

func TestIsolation_CrossTeamDeleteIntegration_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodDelete, "/api/v1/integrations/"+strconv.Itoa(d.intB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA delete teamB integration: got %d, want 403", rec.Code)
	}
	if exist, err := d.c.Integration.Query().Where(entintegration.IDEQ(d.intB)).Exist(t.Context()); err != nil {
		t.Fatalf("check intB exist: %v", err)
	} else if !exist {
		t.Error("intB deleted despite 403 (short-circuit FAILED)")
	}
}

func TestIsolation_OrgWideUserSeesAll(t *testing.T) {
	d := isoSetup(t)
	ctx := t.Context()
	orgRole, err := d.c.Role.Create().
		SetName("admin").SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermIntegrationView)}).Save(ctx)
	if err != nil {
		t.Fatalf("create org role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).SetRoleID(orgRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create org binding: %v", err)
	}
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/integrations/"+strconv.Itoa(d.intB), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("org-wide userA should access teamB integration, got %d", rec.Code)
	}
}
