// middleware.go Echo 鉴权中间件（能力域 13 §6.1）。
//
// 对应 docs/capabilities/09-admin-rbac.md §4.4 + architecture §6.1：
// 所有 API（Web/IM 调用的核心服务）过同一中间件。
// 解析 (user, action, resource) → Authorizer.Check → 通过/拒绝。
//
// 当前用户身份解析：从请求头 X-Vigil-User-ID 取（简化，登录态/IM 映射后续）。
// 权限点与路由的映射：通过 PermissionForRoute 在注册路由时声明。
package auth

import (
	"context"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// ctxKey context 键类型（避免冲突）。
type ctxKey int

const (
	// ctxUser 已鉴权的用户 ID
	ctxUser ctxKey = iota
	// ctxPerms 当前用户在该请求 scope 下的权限集（供 handler 按权限渲染）
	ctxPerms
)

// contextKey 公开类型，供其他包取 context 值。
type contextKey = ctxKey

// Middleware 生成一个鉴权中间件，要求请求具有 perm 权限。
// perm 为空时仅做身份解析（不校验权限，用于公开但需登录的接口）。
func Middleware(authz *Authorizer, perm Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// 1. 解析用户 ID（简化：header；完整登录态后续接入）
			uidStr := c.Request().Header.Get("X-Vigil-User-ID")
			if uidStr == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing user"})
			}
			uid, err := strconv.Atoi(uidStr)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid user id"})
			}
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), ctxUser, uid)))

			// 2. 无需权限点则放行（仅做了身份解析）
			if perm == "" {
				return next(c)
			}

			// 3. 解析资源 scope（从 path param :team_id，若有）
			teamScope := parseTeamScope(c)

			// 4. 鉴权
			ok, err := authz.Check(c.Request().Context(), AuthzRequest{
				UserID:     uid,
				Permission: perm,
				TeamScope:  teamScope,
			})
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "authz failed"})
			}
			if !ok {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
			}
			return next(c)
		}
	}
}

// parseTeamScope 从 :team_id path param 解析团队作用域。
func parseTeamScope(c echo.Context) *int {
	s := c.Param("team_id")
	if s == "" {
		return nil
	}
	id, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &id
}

// UserIDFromContext 从 context 取已鉴权的用户 ID。
func UserIDFromContext(ctx context.Context) (int, bool) {
	uid, ok := ctx.Value(ctxUser).(int)
	return uid, ok
}
