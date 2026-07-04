// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// 时间线按 incident 归属 team 鉴权（kind="incident"）：team 级权限用户不能查看/追加其他
// team 事件的时间线。除断言 403 外，对追加（写）端点回读时间线条目数，专治 checkAccess 短路
// 失效（d98843a 修复类）——「报 403 却已落库」的越权。
package timeline

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
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
	userA, userB int
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:timeline_iso?mode=memory&cache=shared&_fk=1")
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
	return isoData{c: c, teamA: ta.ID, teamB: tb.ID, incA: incA.ID, incB: incB.ID, userA: ua.ID, userB: ub.ID}
}

func newIsolatedHandler(d isoData) *echo.Echo {
	h := NewHandler(NewRecorder(d.c))
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

// TestIsolation_ListOwnTeamTimeline_OK userA 查自己 team 事件的时间线 → 200（基线）。
func TestIsolation_ListOwnTeamTimeline_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(d.incA)+"/timeline", d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("userA list own timeline: got %d, want 200 (baseline)", rec.Code)
	}
}

// TestIsolation_ListOtherTeamTimeline_403 userA 查 teamB 事件的时间线 → 403。
func TestIsolation_ListOtherTeamTimeline_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/timeline", d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA list teamB timeline: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

// TestIsolation_ListOtherTeamTimeline_Symmetric userB 查 teamA 事件的时间线 → 403（对称）。
func TestIsolation_ListOtherTeamTimeline_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(d.incA)+"/timeline", d.userB, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB list teamA timeline: got %d, want 403", rec.Code)
	}
}

// TestIsolation_CrossTeamAddNote_StateUnchanged userA 向 teamB 事件追加备注 → 403 且不落库。
// ★ add 是写端点：仅断言 403 会漏掉 checkAccess 短路失效，必须回读时间线条目数确认未新增。
func TestIsolation_CrossTeamAddNote_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	before, err := NewRecorder(d.c).Count(t.Context(), d.incB)
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/timeline", d.userA, `{"content":"injected note","source":"web"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA add note to teamB incident: got %d, want 403 (cross-team write isolation FAILED)", rec.Code)
	}
	after, err := NewRecorder(d.c).Count(t.Context(), d.incB)
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Errorf("teamB timeline mutated despite 403: count %d → %d (403-but-recorded bug)", before, after)
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
	rec := reqAsUser(e, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(d.incB)+"/timeline", d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("org-wide userA should access teamB timeline, got %d", rec.Code)
	}
}
