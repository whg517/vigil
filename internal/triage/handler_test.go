// handler_test.go 分诊路由 HTTP 端点测试（M6 重路由）。
package triage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// rerouteFixture 一套重路由测试数据：team + service + 一个未路由 Event + 有 route_override 权限的用户。
type rerouteFixture struct {
	c       *ent.Client
	e       *echo.Echo
	svcID   int
	evtID   int
	userID  int
	teamID  int
	otherID int // 另一个 service（跨 team 隔离用）
}

func newRerouteFixture(t *testing.T) rerouteFixture {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:triage_h?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	tm, err := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	tm2, err := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	if err != nil {
		t.Fatalf("team2: %v", err)
	}
	svc, err := c.Service.Create().SetName("payment-api").SetSlug("payment").
		SetTeamID(tm.ID).SetAutoCreateIncident(true).Save(ctx)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	other, err := c.Service.Create().SetName("order-api").SetSlug("order").
		SetTeamID(tm2.ID).SetAutoCreateIncident(true).Save(ctx)
	if err != nil {
		t.Fatalf("other service: %v", err)
	}
	evt, err := c.Event.Create().
		SetSourceEventID("e1").SetSource("prometheus").
		SetSeverity(event.SeverityWarning).SetStatus(event.StatusFiring).
		SetSummary("孤儿").SetLabels(map[string]string{"foo": "bar"}).
		SetDedupKey("e1").Save(ctx)
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	// 用户：仅 teamA 的 service.route_override。
	u, err := c.User.Create().SetUsername("alice").SetEmail("a@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	rr, err := c.Role.Create().SetName("router").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermServiceRouteOverride)}).Save(ctx)
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if _, err := c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rr.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(tm.ID)).
		SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("binding: %v", err)
	}

	h := NewHandler(c, NewEngine(c, nil))
	h.SetAuthorizer(auth.NewAuthorizer(c))
	h.SetScopeResolver(auth.NewScopeResolver(c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)

	return rerouteFixture{c: c, e: e, svcID: svc.ID, evtID: evt.ID, userID: u.ID, teamID: tm.ID, otherID: other.ID}
}

func (f rerouteFixture) post(uid int, evtID int, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/"+strconv.Itoa(evtID)+"/reroute", strings.NewReader(body))
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

// TestReroute_Success 有权限用户把未路由 Event 指派到本团队 Service → 200 + 建单。
func TestReroute_Success(t *testing.T) {
	f := newRerouteFixture(t)
	rec := f.post(f.userID, f.evtID, `{"service_id":`+strconv.Itoa(f.svcID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reroute: got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "incident_created") {
		t.Errorf("want incident_created, body=%s", rec.Body.String())
	}
}

// TestReroute_CrossTeamForbidden 指派到无权限的其它 team Service → 403。
func TestReroute_CrossTeamForbidden(t *testing.T) {
	f := newRerouteFixture(t)
	rec := f.post(f.userID, f.evtID, `{"service_id":`+strconv.Itoa(f.otherID)+`}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-team reroute: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestReroute_ServiceNotFound 目标 service 不存在 → 404。
func TestReroute_ServiceNotFound(t *testing.T) {
	f := newRerouteFixture(t)
	rec := f.post(f.userID, f.evtID, `{"service_id":99999}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("service not found: got %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestReroute_MissingServiceID 缺 service_id → 400。
func TestReroute_MissingServiceID(t *testing.T) {
	f := newRerouteFixture(t)
	rec := f.post(f.userID, f.evtID, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing service_id: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestReroute_AlreadyRoutedConflict 已路由的 Event 重路由 → 409。
func TestReroute_AlreadyRoutedConflict(t *testing.T) {
	f := newRerouteFixture(t)
	// 先绑定该 Event 到 svc。
	if err := f.c.Event.UpdateOneID(f.evtID).SetServiceID(f.svcID).Exec(context.Background()); err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	rec := f.post(f.userID, f.evtID, `{"service_id":`+strconv.Itoa(f.svcID)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("already routed: got %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
}
