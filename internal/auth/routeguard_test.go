// routeguard_test.go 路由级权限守卫测试（QA 审计 C1 RBAC 接线）。
//
// 审计发现：RequirePermPerRoute 定义了却从未被调用，所有写路由对任意登录用户敞开。
// 本测试直接验证 RouteGuard 中间件：命中登记路由时按权限点鉴权，未登记放行，
// 有权限通过 / 无权限 403 / authz 为 nil 降级放行。
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// TestRouteGuard_Allowed 用户拥有登记的权限点 → 通过。
func TestRouteGuard_Allowed(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:rg_allowed?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	_, _ = db.User.Create().SetUsername("u").SetEmail("u@v.l").Save(ctx)
	_, _ = db.Role.Create().SetName("r").SetScopeLevel(role.ScopeLevelOrg).SetPermissions([]string{string(PermIncidentEscalate)}).Save(ctx)
	_, _ = db.RoleBinding.Create().SetUserID(1).SetRoleID(1).SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx)

	authz := NewAuthorizer(db)
	g := NewRouteGuard(authz, nil) // resolver=nil：uid 已由 RequireUser 注入 context
	g.RoutePerm(http.MethodPost, "/incidents/:id/escalate", PermIncidentEscalate)

	e := echo.New()
	called := false
	e.POST("/incidents/:id/escalate", func(c *echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	}, g.Middleware())

	req := httptest.NewRequest(http.MethodPost, "/incidents/5/escalate", nil)
	// 模拟 RequireUser 已注入 uid=1
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, 1))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (user has permission); body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("handler not called despite permission granted")
	}
}

// TestRouteGuard_Forbidden 用户无登记的权限点 → 403。
// 这是审计 C1 的核心断言：低权用户访问敏感写路由应被拒绝。
func TestRouteGuard_Forbidden(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:rg_forbidden?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	_, _ = db.User.Create().SetUsername("u").SetEmail("u@v.l").Save(ctx)
	// user 1 绑定的角色只有 incident.view，没有 incident.escalate
	_, _ = db.Role.Create().SetName("r").SetScopeLevel(role.ScopeLevelOrg).SetPermissions([]string{string(PermIncidentView)}).Save(ctx)
	_, _ = db.RoleBinding.Create().SetUserID(1).SetRoleID(1).SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx)

	authz := NewAuthorizer(db)
	g := NewRouteGuard(authz, nil)
	g.RoutePerm(http.MethodPost, "/incidents/:id/escalate", PermIncidentEscalate)

	e := echo.New()
	called := false
	e.POST("/incidents/:id/escalate", func(c *echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	}, g.Middleware())

	req := httptest.NewRequest(http.MethodPost, "/incidents/5/escalate", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, 1))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (user lacks permission); body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("handler should NOT be called when permission denied")
	}
}

// TestRouteGuard_UnregisteredPass 未登记的路由放行（渐进启用策略）。
func TestRouteGuard_UnregisteredPass(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:rg_unreg?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })

	authz := NewAuthorizer(db)
	g := NewRouteGuard(authz, nil) // 不登记任何路由

	e := echo.New()
	called := false
	e.GET("/anything", func(c *echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	}, g.Middleware())

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, 1))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (unregistered route passes); body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("unregistered route handler should be called")
	}
}

// TestRouteGuard_NilAuthzDegrades authz 为 nil 时整体放行（降级，不阻断）。
func TestRouteGuard_NilAuthzDegrades(t *testing.T) {
	g := NewRouteGuard(nil, nil)
	g.RoutePerm(http.MethodPost, "/x", PermIncidentView) // 即使登记了

	e := echo.New()
	called := false
	e.POST("/x", func(c *echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	}, g.Middleware())

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (nil authz degrades to allow); body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("handler should be called when authz is nil (degrade)")
	}
}
