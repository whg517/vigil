// handler.go WebSocket 连接处理（能力域 8 §状态双向同步）。
//
// 端点：GET /ws/incidents/:id —— 订阅某 incident 的实时变更。
// 流程：握手鉴权 → 升级为 WS → 订阅 hub → 读循环（保活/处理客户端消息）+ 写循环（推送）→ 关闭退订。
//
// 鉴权（T0.5，安全修复）：
//
//	WS 端点原先无任何身份/权限校验——任意匿名连接都能订阅任意 incident 的状态推送，
//	等于把 incident 详情实时流对全网敞开（水平越权 + 匿名读）。此为必须堵的安全缺陷。
//
//	浏览器 WebSocket 握手无法携带自定义 Authorization 头（EventSource/WebSocket API 限制），
//	因此令牌走 ?token=<jwt> query 传递，在 Upgrade 之前完成校验：
//	  1. 从 ?token= 取 JWT → 复用 IdentityResolver 解析出 uid（同 Web/IM 的 JWT 链路，
//	     含签名/过期/类型/改密吊销校验）；无 token 或无效 → 401（不 Upgrade）。
//	  2. 用 Authorizer + ScopeResolver 反查 incident 归属 team → 校验 uid 的 incident.view
//	     权限（团队软隔离，与 Web/IM 同一条鉴权链）；无权 → 403（不 Upgrade）。
//	  3. 通过后再 Upgrade + Subscribe，保持既有推送逻辑不变。
//
//	鉴权失败一律在 Upgrade 前以标准 HTTP 401/403 返回——此时仍是普通 HTTP 请求，
//	不能先 Upgrade 再关连接（那样客户端已看到 101，且泄露了端点可达性）。
package ws

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kevin/vigil/internal/auth"
	"github.com/labstack/echo/v5"
)

// Handler WebSocket 连接 handler。
//
// authz/resolver/scope 为握手鉴权依赖（T0.5）：任一为 nil 时握手鉴权会拒绝所有连接
// （fail-closed），避免装配缺失时静默退回"无鉴权"的旧不安全行为。
type Handler struct {
	hub      *Hub
	upgrader websocket.Upgrader
	// authz 鉴权器：判定 uid 是否拥有目标 incident 的 incident.view 权限。
	authz *auth.Authorizer
	// resolver 身份解析器：从握手请求（这里是 ?token= 构造的 Bearer 头）解析 uid。
	resolver *auth.IdentityResolver
	// scope 资源级 scope 解析器：反查 incident 归属 team，供团队软隔离判定。
	scope *auth.ScopeResolver
}

// NewHandler 创建 handler。
//
//	hub      广播中心（全局单例）
//	authz    RBAC 鉴权器（校验 incident.view）
//	resolver 身份解析器（解析握手 JWT）
//	scope    资源级 scope 解析器（反查 incident → team）
//
// 三个鉴权依赖用于握手阶段（Upgrade 前）校验；缺任一则所有握手被拒（fail-closed）。
func NewHandler(hub *Hub, authz *auth.Authorizer, resolver *auth.IdentityResolver, scope *auth.ScopeResolver) *Handler {
	return &Handler{
		hub:      hub,
		authz:    authz,
		resolver: resolver,
		scope:    scope,
		upgrader: websocket.Upgrader{
			// 允许跨域（开发态前端走 vite proxy 同源，生产按部署配置收敛）。
			// 自托管场景下 VIGIL 前端与 API 同源，CheckOrigin 用默认即可。
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Register 挂载 WS 路由。挂在 public group：因为握手鉴权在 handler 内部按 ?token= 完成，
// 而组级 RouteGuard 中间件只认 Authorization 头 / 读不到 query token，无法复用。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/ws/incidents/:id", h.handleIncident)
	// 看板订阅（值班大屏/仪表盘实时化）：org 级只读，握手要求 org 级 analytics.view。
	g.GET("/ws/dashboard", h.handleDashboard)
}

// handleIncident 处理 incident 订阅连接。
// 先做握手鉴权（身份 + incident.view 权限），通过后再升级为 WS 并持续推送该 incident 的变更。
func (h *Handler) handleIncident(c *echo.Context) error {
	incidentID, err := strconv.Atoi(c.Param("id"))
	if err != nil || incidentID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid incident id"})
	}

	// —— 握手鉴权（必须在 Upgrade 之前，以标准 HTTP 状态码返回）——
	// 依赖缺失即拒绝（fail-closed）：宁可全拒也不退回无鉴权旧行为。
	if h.resolver == nil || h.authz == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	// 1. 从 ?token= 取 JWT，构造 Authorization: Bearer 头复用 IdentityResolver 的 JWT 链路
	//    （签名/过期/token_type/改密吊销校验一致，不自造解析）。
	//    浏览器 WS API 无法带自定义头，故令牌只能走 query。
	token := c.QueryParam("token")
	if token == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing token"})
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	uid, ok := h.resolver.Resolve(c.Request().Context(), header)
	if !ok || uid <= 0 {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
	}

	// 2. 校验 uid 对该 incident 的 incident.view 权限（团队软隔离，同 Web/IM 鉴权链）。
	//    CheckResourceAccess 反查 incident 归属 team → authz.Check；跨 team 无权即 false。
	//    注：uid 此处已 >0（上一步保证），不会命中 CheckResourceAccess 的匿名放行分支。
	allowed, err := auth.CheckResourceAccess(
		c.Request().Context(), h.authz, h.scope, uid, auth.PermIncidentView, "incident", incidentID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "authz failed"})
	}
	if !allowed {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	// —— 鉴权通过，升级为 WebSocket 并订阅该 incident ——
	return h.serve(c, func(cli *client) func() { return h.hub.Subscribe(incidentID, cli) })
}

// handleDashboard 处理看板订阅连接（值班大屏/仪表盘实时化）。
//
// 大屏定位为 org 级只读 NOC 看板：握手要求 org 级 analytics.view（TeamScope=nil），
// 通过后订阅 hub 的看板 topic，持续收到任一 incident 生命周期事件的轻量增量推送。
// 不针对具体 incident，故无 team 软隔离资源判定——org 级 analytics.view 即全局可见。
func (h *Handler) handleDashboard(c *echo.Context) error {
	// —— 握手鉴权（Upgrade 前，标准 HTTP 状态码）；依赖缺失即拒（fail-closed）——
	if h.resolver == nil || h.authz == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	token := c.QueryParam("token")
	if token == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing token"})
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	uid, ok := h.resolver.Resolve(c.Request().Context(), header)
	if !ok || uid <= 0 {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
	}

	// org 级 analytics.view：TeamScope=nil 只查 org 级 binding。看板是全组织只读视图，
	// 仅 org 级角色可订阅（team 级 Leader 走各自 Web 仪表盘的拉取式 team scope 数据）。
	allowed, err := h.authz.Check(c.Request().Context(), auth.AuthzRequest{
		UserID:     uid,
		Permission: auth.PermAnalyticsView,
		TeamScope:  nil,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "authz failed"})
	}
	if !allowed {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	return h.serve(c, func(cli *client) func() { return h.hub.SubscribeDashboard(cli) })
}

// serve 升级为 WebSocket、按 subscribe 订阅、跑读/写循环，连接关闭时退订清理。
// subscribe 由调用方决定订阅什么（per-incident 或看板 topic），其余生命周期逻辑统一。
func (h *Handler) serve(c *echo.Context, subscribe func(*client) func()) error {
	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		// Upgrade 失败已由 upgrader 写了 HTTP 错误响应，此处返回 nil 避免重复写
		return nil
	}
	defer func() { _ = conn.Close() }()

	// 订阅 + 注册客户端
	cli := newClient()
	unsubscribe := subscribe(cli)
	defer unsubscribe()

	// 写循环：把 hub 广播的消息写给客户端。
	// 读循环退出时 defer close(cli.send) 关闭 channel，写循环随之退出（无 goroutine 泄漏）。
	go func() {
		for msg := range cli.send {
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return // 写失败（客户端断开），退出
			}
		}
	}()

	// 读循环：读客户端消息（保活；客户端断开时 ReadMessage 返回 error）。
	// 本期不处理客户端→服务端消息（仅推送），读循环只为感知断开。
	defer func() {
		close(cli.send) // 关闭 send channel，写循环退出
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			return nil // 客户端断开/超时，退出清理（Upgrade 后响应已写，返回 nil 避免重复写）
		}
	}
}
