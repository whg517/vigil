// handler.go WebSocket 连接处理（能力域 8 §状态双向同步）。
//
// 端点：GET /ws/incidents/:id —— 订阅某 incident 的实时变更。
// 流程：升级为 WS → 订阅 hub → 读循环（保活/处理客户端消息）+ 写循环（推送）→ 关闭退订。
package ws

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// Handler WebSocket 连接 handler。
type Handler struct {
	hub      *Hub
	upgrader websocket.Upgrader
}

// NewHandler 创建 handler。hub 为广播中心（全局单例）。
func NewHandler(hub *Hub) *Handler {
	return &Handler{
		hub: hub,
		upgrader: websocket.Upgrader{
			// 允许跨域（开发态前端走 vite proxy 同源，生产按部署配置收敛）。
			// 自托管场景下 VIGIL 前端与 API 同源，CheckOrigin 用默认即可。
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Register 挂载 WS 路由。建议挂到 public group（WS 连接自带身份头，与 HTTP 一致）。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/ws/incidents/:id", h.handleIncident)
}

// handleIncident 处理 incident 订阅连接。
// 客户端连接后持续接收该 incident 的状态变更推送，直到断开。
func (h *Handler) handleIncident(c echo.Context) error {
	incidentID, err := strconv.Atoi(c.Param("id"))
	if err != nil || incidentID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid incident id"})
	}

	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		// Upgrade 失败已由 upgrader 写了 HTTP 错误响应，此处返回 nil 避免重复写
		return nil
	}
	defer func() { _ = conn.Close() }()

	// 订阅 + 注册客户端
	cli := newClient()
	unsubscribe := h.hub.Subscribe(incidentID, cli)
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
