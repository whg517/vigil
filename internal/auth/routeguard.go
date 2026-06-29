// routeguard.go 路由级权限守卫（能力域 13 §4.4，QA 审计 C1）。
//
// 背景：原实现只在 v1 组挂 RequireUser（仅身份解析），RequirePermPerRoute
// 定义了却从未被调用——所有写路由（创建角色 / 删除复盘 / 升级事件 / 签发 API Key 等）
// 对任意登录用户敞开，等同无访问控制（审计 Critical C1）。
//
// 本文件提供 RouteGuard：一个按 (method, path) 查权限点的 echo 中间件。
// 各 handler 通过 SetRouteGuard 注入守卫并调用 g.RoutePerm(...) 登记"敏感写路由→权限点"
// 映射；中间件在请求匹配到登记项时执行 Authorizer.Check，未登记的路由保持现状
// （渐进启用，避免一次性阻断全部接口）。
//
// 设计取舍：
//   - 不修改 Register 签名（覆盖面大、风险高），改用各域 handler 自报敏感路由表。
//   - 守卫为 nil（测试 / 未注入）时 RoutePerm 为 no-op，保证向后兼容。
//   - 资源 scope（team_id）由 parseTeamScope 从 path param 解析。
package auth

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

// RouteGuard 路由级权限守卫工厂。
// Handler 持有它后调用 RoutePerm 注册敏感路由的权限点；
// 中间件在请求时查表，命中则鉴权。
//
// 返回值同时作为 echo.MiddlewareFunc 挂到组上：对所有请求查表，
// 命中登记项才鉴权，未登记放行（渐进启用策略）。
type RouteGuard struct {
	authz    *Authorizer
	resolver *IdentityResolver
	routes   map[string]Permission // key = METHOD + " " + cleanPath
}

// NewRouteGuard 创建路由级权限守卫。authz 为 nil 时守卫不生效（降级放行）。
func NewRouteGuard(authz *Authorizer, resolver *IdentityResolver) *RouteGuard {
	return &RouteGuard{
		authz:    authz,
		resolver: resolver,
		routes:   make(map[string]Permission),
	}
}

// routeKey 构造路由表键（METHOD 大写 + 空格 + path）。
func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}

// RoutePerm 登记一条路由所需的权限点。guard 为 nil 时 no-op（向后兼容）。
// 同时返回传入的 middleware（若需 per-route 显式挂载）。
func (g *RouteGuard) RoutePerm(method, path string, perm Permission) {
	if g == nil || g.routes == nil {
		return
	}
	g.routes[routeKey(method, path)] = perm
}

// Middleware 返回挂到 echo.Group 的中间件：对每个请求查表，命中则鉴权。
// 未登记的路由放行（渐进启用）。authz 为 nil 时整体放行（降级）。
func (g *RouteGuard) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if g == nil || g.authz == nil {
				return next(c)
			}
			perm, ok := g.routes[routeKey(c.Request().Method, c.Path())]
			if !ok {
				// 未登记为敏感路由 → 放行（仍受组级 RequireUser 身份解析保护）
				return next(c)
			}
			// 命中敏感路由：必须已解析身份（组级 RequireUser 已注入 uid）
			uid, ok := UserIDFromContext(c.Request().Context())
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
			}
			teamScope := parseTeamScope(c)
			allowed, err := g.authz.Check(c.Request().Context(), AuthzRequest{
				UserID:     uid,
				Permission: perm,
				TeamScope:  teamScope,
			})
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "authz failed"})
			}
			if !allowed {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
			}
			return next(c)
		}
	}
}
