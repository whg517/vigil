// handler_test.go T4.4 订阅管理端点测试：
//   - 登录用户创建/列出/删除自己的订阅
//   - 只能删自己的订阅（他人的 404）
//   - team/service 二选一校验
//   - 可见性校验：不能订阅不可见团队的资源
package subscription

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	entsubscription "github.com/kevin/vigil/ent/subscription"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// hData 订阅端点测试场景：teamA/teamB，userA 仅 teamA 可见（subscriber 绑定）。
type hData struct {
	c            *ent.Client
	teamA, teamB int
	svcA         int
	userA, userB int
}

func hSetup(t *testing.T) hData {
	t.Helper()
	c := enttestOpen(t)
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	ta, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	tb, _ := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	svcA, _ := c.Service.Create().SetName("checkout").SetSlug("chk").SetTeamID(ta.ID).Save(ctx)

	subRole, _ := c.Role.Create().
		SetName("subscriber").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermIncidentView)}).Save(ctx)

	ua, _ := c.User.Create().SetUsername("alice").SetEmail("a@x.io").Save(ctx)
	ub, _ := c.User.Create().SetUsername("bob").SetEmail("b@x.io").Save(ctx)
	// userA 绑 teamA（可见 teamA），userB 绑 teamB。
	for _, p := range []struct{ uid, tid int }{{ua.ID, ta.ID}, {ub.ID, tb.ID}} {
		_, _ = c.RoleBinding.Create().
			SetUserID(p.uid).SetRoleID(subRole.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).
			SetTeamID(strconv.Itoa(p.tid)).
			SetGrantedAt(time.Now()).Save(ctx)
	}
	return hData{c: c, teamA: ta.ID, teamB: tb.ID, svcA: svcA.ID, userA: ua.ID, userB: ub.ID}
}

func newHandlerEcho(d hData) *echo.Echo {
	h := NewHandler(d.c)
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
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

// TestCreate_TeamSubscription_OK 用户订阅自己可见的团队 → 201 并落库。
func TestCreate_TeamSubscription_OK(t *testing.T) {
	d := hSetup(t)
	e := newHandlerEcho(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/subscriptions", d.userA,
		`{"team_id":`+strconv.Itoa(d.teamA)+`,"min_severity":"critical"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// 落库校验：共有一条订阅。
	n, _ := d.c.Subscription.Query().Count(t.Context())
	if n != 1 {
		t.Errorf("expected 1 subscription persisted, got %d", n)
	}
	var sub ent.Subscription
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	if sub.MinSeverity != entsubscription.MinSeverityCritical {
		t.Errorf("expected min_severity critical, got %s", sub.MinSeverity)
	}
}

// TestCreate_ServiceSubscription_OK 用户订阅自己可见团队下的服务 → 201。
func TestCreate_ServiceSubscription_OK(t *testing.T) {
	d := hSetup(t)
	e := newHandlerEcho(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/subscriptions", d.userA,
		`{"service_id":`+strconv.Itoa(d.svcA)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreate_BothScopes_400 team_id 与 service_id 同传 → 400（二选一）。
func TestCreate_BothScopes_400(t *testing.T) {
	d := hSetup(t)
	e := newHandlerEcho(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/subscriptions", d.userA,
		`{"team_id":`+strconv.Itoa(d.teamA)+`,"service_id":`+strconv.Itoa(d.svcA)+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for both scopes, got %d", rec.Code)
	}
}

// TestCreate_NeitherScope_400 都不传 → 400。
func TestCreate_NeitherScope_400(t *testing.T) {
	d := hSetup(t)
	e := newHandlerEcho(d)
	rec := reqAsUser(e, http.MethodPost, "/api/v1/subscriptions", d.userA, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for neither scope, got %d", rec.Code)
	}
}

// TestCreate_InvisibleTeam_403 订阅不可见团队 → 403 且不落库。
func TestCreate_InvisibleTeam_403(t *testing.T) {
	d := hSetup(t)
	e := newHandlerEcho(d)
	// userA 不可见 teamB。
	rec := reqAsUser(e, http.MethodPost, "/api/v1/subscriptions", d.userA,
		`{"team_id":`+strconv.Itoa(d.teamB)+`}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for invisible team, got %d: %s", rec.Code, rec.Body.String())
	}
	n, _ := d.c.Subscription.Query().Count(t.Context())
	if n != 0 {
		t.Errorf("forbidden subscription should not be persisted, got %d rows", n)
	}
}

// TestList_OwnOnly 列表只返回自己的订阅。
func TestList_OwnOnly(t *testing.T) {
	d := hSetup(t)
	ctx := t.Context()
	// userA 一条 teamA 订阅；userB 一条 teamB 订阅。
	_, _ = d.c.Subscription.Create().SetUserID(d.userA).SetTeamID(d.teamA).Save(ctx)
	_, _ = d.c.Subscription.Create().SetUserID(d.userB).SetTeamID(d.teamB).Save(ctx)

	e := newHandlerEcho(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/subscriptions", d.userA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var subs []ent.Subscription
	_ = json.Unmarshal(rec.Body.Bytes(), &subs)
	if len(subs) != 1 {
		t.Fatalf("expected 1 own subscription, got %d", len(subs))
	}
}

// TestDelete_OwnOK_OthersNotFound 删自己的 204；删他人的 404（不落删）。
func TestDelete_OwnOK_OthersNotFound(t *testing.T) {
	d := hSetup(t)
	ctx := t.Context()
	mine, _ := d.c.Subscription.Create().SetUserID(d.userA).SetTeamID(d.teamA).Save(ctx)
	theirs, _ := d.c.Subscription.Create().SetUserID(d.userB).SetTeamID(d.teamB).Save(ctx)

	e := newHandlerEcho(d)
	// 删自己的：204。
	rec := reqAsUser(e, http.MethodDelete, "/api/v1/subscriptions/"+strconv.Itoa(mine.ID), d.userA, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting own subscription, got %d", rec.Code)
	}
	// userA 删 userB 的：404，且 theirs 仍在。
	rec = reqAsUser(e, http.MethodDelete, "/api/v1/subscriptions/"+strconv.Itoa(theirs.ID), d.userA, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 deleting others' subscription, got %d", rec.Code)
	}
	if exists, _ := d.c.Subscription.Query().Where(entsubscription.IDEQ(theirs.ID)).Exist(ctx); !exists {
		t.Error("others' subscription should NOT be deleted by userA")
	}
}
