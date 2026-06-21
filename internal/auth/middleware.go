// middleware.go Echo 鉴权中间件（能力域 13 §6.1）。
//
// 对应 docs/capabilities/09-admin-rbac.md §4.4 + architecture §6.1：
// 所有 API（Web/IM 调用的核心服务）过同一中间件。
// 解析 (user, action, resource) → Authorizer.Check → 通过/拒绝。
//
// 身份解析优先级（JWT 登录态引入后）：
//  1. Authorization: Bearer <jwt> —— 校验 JWT 拿 userID（JWT 链路，优先）；
//  2. 回退 X-Vigil-User-ID 头（兼容现有部署 / AUTH_ENABLED=false 阶段）。
//
// 安全约束：若请求带了 Bearer 但 JWT 无效，不回退 header（避免伪造降级），直接判无身份。
package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"

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
// signer 非 nil 时启用 JWT 身份解析；为 nil 时仅 X-Vigil-User-ID（旧链路）。
func Middleware(authz *Authorizer, perm Permission, signer *JWTSigner) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// 1. 解析用户 ID（JWT 优先，回退 header）
			uid, ok := resolveUserID(c, signer)
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

// resolveUserID 解析用户 ID。JWT 优先，回退 header。返回 (uid, ok)。
// 安全约束：JWT 存在但无效时不回退 header（避免攻击者用伪造 Bearer 降级到可伪造的 header）。
func resolveUserID(c echo.Context, signer *JWTSigner) (int, bool) {
	// 1. JWT 分支
	if signer != nil {
		authz := c.Request().Header.Get("Authorization")
		// 大小写不敏匹配 "Bearer " 前缀
		if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
			claims, err := signer.ParseToken(strings.TrimSpace(authz[7:]))
			if err == nil && claims.TokenType == TokenTypeAccess {
				return claims.UserID, true
			}
			// JWT 存在但无效：不回退 header，直接判无身份
			return 0, false
		}
	}
	// 2. 回退 X-Vigil-User-ID（兼容）
	uidStr := c.Request().Header.Get("X-Vigil-User-ID")
	if uidStr == "" {
		return 0, false
	}
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		return 0, false
	}
	return uid, true
}

// RequireUser 仅做身份解析（不校验权限），用于"需登录但任何角色可访问"的接口。
// 身份解析顺序：JWT 优先，回退 X-Vigil-User-ID。
// 无任何有效身份时按 enforce 决定：enforce=true 返回 401；enforce=false 放行（匿名，渐进启用）。
// signer 非 nil 时启用 JWT；为 nil 时仅 header（保持旧调用兼容，但建议总是注入 signer）。
func RequireUser(enforce bool, signer *JWTSigner) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid, ok := resolveUserID(c, signer)
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
// 用法：g.GET("/incidents", h.List, auth.RequirePermPerRoute(authz, signer, auth.PermIncidentView))
// 与 Middleware 的区别：perm 通过参数传入，便于 main 按路由精细挂载。
func RequirePermPerRoute(authz *Authorizer, signer *JWTSigner, perm Permission) echo.MiddlewareFunc {
	return Middleware(authz, perm, signer)
}

// UserIDFromContext 从 context 取已鉴权的用户 ID（RequireUser/Middleware 注入）。
// 供 handler 取当前用户；未鉴权时返回 (0, false)。
func UserIDFromContext(ctx context.Context) (int, bool) {
	uid, ok := ctx.Value(ctxUser).(int)
	return uid, ok
}
