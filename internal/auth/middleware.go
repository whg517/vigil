// middleware.go Echo 鉴权中间件（能力域 13 §6.1）。
//
// 对应 docs/capabilities/09-admin-rbac.md §4.4 + architecture §6.1：
// 所有 API（Web/IM 调用的核心服务）过同一中间件。
// 解析 (user, action, resource) → Authorizer.Check → 通过/拒绝。
//
// 身份解析委托给 IdentityResolver（三轨：JWT / API Key / X-Vigil-User-ID）。
// 安全约束见 resolver.go：凭证存在但无效时不回退低优凭证（防伪造降级）。
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
)

// Middleware 生成一个鉴权中间件，要求请求具有 perm 权限。
// perm 为空时仅做身份解析（不校验权限，用于公开但需登录的接口）。
// resolver 为身份解析器（JWT/APIKey/header 三轨）；为 nil 时仅 header。
func Middleware(authz *Authorizer, perm Permission, resolver *IdentityResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// 1. 解析用户 ID（三轨）
			uid, ok := resolver.Resolve(c.Request().Context(), c.Request().Header)
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing or invalid credentials"})
			}
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), ctxUser, uid)))

			// 2. 无需权限点则放行（仅做了身份解析）
			if perm == "" {
				return next(c)
			}

			// 3. 解析资源 scope（从 path param :team_id，若有）
			teamScope := parseTeamScope(c)

			// 4. 鉴权
			ok2, err := authz.Check(c.Request().Context(), AuthzRequest{
				UserID:     uid,
				Permission: perm,
				TeamScope:  teamScope,
			})
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "authz failed"})
			}
			if !ok2 {
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

// RequireUser 仅做身份解析（不校验权限），用于"需登录但任何角色可访问"的接口。
// 无任何有效身份时按 enforce 决定：enforce=true 返回 401；enforce=false 放行（匿名，渐进启用）。
func RequireUser(enforce bool, resolver *IdentityResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid, ok := resolver.Resolve(c.Request().Context(), c.Request().Header)
			if !ok {
				if enforce {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing or invalid credentials"})
				}
				return next(c) // 匿名放行（渐进启用阶段）
			}
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), ctxUser, uid)))
			return next(c)
		}
	}
}

// RequirePermPerRoute 按路由声明权限的中间件工厂。
// 用法：g.GET("/incidents", h.List, auth.RequirePermPerRoute(authz, resolver, auth.PermIncidentView))
func RequirePermPerRoute(authz *Authorizer, resolver *IdentityResolver, perm Permission) echo.MiddlewareFunc {
	return Middleware(authz, perm, resolver)
}

// UserIDFromContext 从 context 取已鉴权的用户 ID（RequireUser/Middleware 注入）。
// 供 handler 取当前用户；未鉴权时返回 (0, false)。
func UserIDFromContext(ctx context.Context) (int, bool) {
	uid, ok := ctx.Value(ctxUser).(int)
	return uid, ok
}
