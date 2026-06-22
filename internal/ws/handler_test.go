package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/labstack/echo/v5"
)

// TestHandleIncident_UpgradeThroughMetricsMiddleware 是 v5 迁移的关键回归测试（C1）：
//
// metrics.EchoMiddleware 全局挂载，会把 c.Response() 包成 statusRecorder。
// gorilla Upgrader.Upgrade 需要底层 ResponseWriter 实现 http.Hijacker 才能接管 TCP 连接
// 做 WebSocket 握手。statusRecorder 仅嵌入 http.ResponseWriter（不含 Hijacker 方法），
// 靠 Go 匿名字段方法提升 + Unwrap 链透传 Hijacker。
//
// 若透传失效（例如未来重构 statusRecorder 破坏了嵌入），此测试会因 Upgrade 返回
// "response does not implement http.Hijacker" 而失败。
//
// 用 httptest.NewServer（真实 OS HTTP server，ResponseWriter 实现了 Hijacker）而非
// httptest.NewRecorder（后者也实现 Hijacker 但不走真实网络栈，无法验证完整握手）。
func TestHandleIncident_UpgradeThroughMetricsMiddleware(t *testing.T) {
	hub := NewHub()
	wsHandler := NewHandler(hub)

	e := echo.New()
	e.Use(metrics.EchoMiddleware()) // ★ 关键：模拟生产中间件链
	wsHandler.Register(e.Group(""))

	// 用真实 HTTP server（其 ResponseWriter 实现 http.Hijacker）。
	srv := httptest.NewServer(e)
	defer srv.Close()

	// 把 http:// 换成 ws://。
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/incidents/42"

	// 客户端拨号 + 完成握手。若 Hijacker 透传失效，这里直接报错。
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		body := ""
		if resp != nil {
			body = resp.Status
			_ = resp.Body.Close()
		}
		t.Fatalf("WebSocket 握手失败（statusRecorder 未透传 http.Hijacker？）: %v [resp=%s]", err, body)
	}
	defer conn.Close()
	defer func() { _ = resp.Body.Close() }()

	// 验证握手响应是 101 Switching Protocols（确认 Upgrade 成功）。
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("握手状态码 = %d, want 101", resp.StatusCode)
	}

	// 等订阅注册到 hub（Subscribe 在 handleIncident 内异步完成）。
	// 用「广播 + 读」的带重试探测：读到即订阅已就绪。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got string
	for i := 0; i < 20; i++ {
		hub.BroadcastIncident(42, "ack", nil)
		if _, msg, err := conn.ReadMessage(); err == nil {
			got = string(msg)
			break
		}
	}
	if got == "" {
		t.Fatal("3s 内未收到广播消息（订阅未就绪或写循环失效）")
	}
	if !strings.Contains(got, "incident_changed") {
		t.Errorf("收到的消息不含预期 type: %s", got)
	}
}

// TestHandleIncident_InvalidID 非法 incident id 返回 400，不进入 Upgrade 路径。
// 顺带覆盖 handleIncident 的参数校验分支。
func TestHandleIncident_InvalidID(t *testing.T) {
	hub := NewHub()
	wsHandler := NewHandler(hub)

	e := echo.New()
	wsHandler.Register(e.Group(""))

	req := httptest.NewRequest(http.MethodGet, "/ws/incidents/abc", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 id 状态码 = %d, want 400", rec.Code)
	}
}
