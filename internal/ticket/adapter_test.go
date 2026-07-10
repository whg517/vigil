package ticket

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebhookAdapter_CreateTicket_PostsAndParsesURL 通用 webhook 适配器 POST 请求并解析 tracker_url。
func TestWebhookAdapter_CreateTicket_PostsAndParsesURL(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"tracker_url":"https://tickets.example.com/T-1","external_id":"T-1"}`))
	}))
	defer srv.Close()

	a := NewWebhookAdapter(true) // 测试放行私网（httptest 本地）
	cfg := AdapterConfig{Endpoint: srv.URL, Credential: "secret-token", Config: map[string]any{"project": "OPS"}}
	req := TicketRequest{Title: "补监控", Description: "补监控", ActionItemID: 7}
	res, err := a.CreateTicket(context.Background(), cfg, req)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if res.TrackerURL != "https://tickets.example.com/T-1" {
		t.Errorf("tracker_url: got %q", res.TrackerURL)
	}
	if res.ExternalID != "T-1" {
		t.Errorf("external_id: got %q", res.ExternalID)
	}
	// 凭据只进 Authorization 头，不进 body。
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header: got %q, want Bearer secret-token", gotAuth)
	}
	if raw, _ := json.Marshal(gotBody); strings.Contains(string(raw), "secret-token") {
		t.Error("credential leaked into request body")
	}
	// config.project 透传进 payload。
	if gotBody["project"] != "OPS" {
		t.Errorf("project not passed through: got %v", gotBody["project"])
	}
}

// TestWebhookAdapter_Unreachable_ReturnsError 目标不可达时返回 error（供 Engine best-effort 吞掉）。
func TestWebhookAdapter_Unreachable_ReturnsError(t *testing.T) {
	a := NewWebhookAdapter(true)
	// 关闭的端口（拨号失败）。
	cfg := AdapterConfig{Endpoint: "http://127.0.0.1:1"}
	_, err := a.CreateTicket(context.Background(), cfg, TicketRequest{Title: "x"})
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}

// TestWebhookAdapter_BadStatus_ReturnsError 非 2xx 返回 error。
func TestWebhookAdapter_BadStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := NewWebhookAdapter(true)
	_, err := a.CreateTicket(context.Background(), AdapterConfig{Endpoint: srv.URL}, TicketRequest{Title: "x"})
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

// TestWebhookAdapter_InvalidEndpoint_Blocked scheme/host 非法被 SSRF 静态校验拦截。
func TestWebhookAdapter_InvalidEndpoint_Blocked(t *testing.T) {
	a := NewWebhookAdapter(true)
	for _, ep := range []string{"", "file:///etc/passwd", "ftp://x/y"} {
		if _, err := a.CreateTicket(context.Background(), AdapterConfig{Endpoint: ep}, TicketRequest{Title: "x"}); err == nil {
			t.Errorf("endpoint %q should be blocked", ep)
		}
	}
}

// TestSSRF_BlocksPrivateInProduction 生产模式（allowPrivate=false）拦截私网/元数据地址。
func TestSSRF_BlocksPrivateInProduction(t *testing.T) {
	a := NewWebhookAdapter(false) // 生产：禁私网
	// 169.254.169.254 云元数据 —— 连接时被 dialer Control 拦截。
	_, err := a.CreateTicket(context.Background(),
		AdapterConfig{Endpoint: "http://169.254.169.254/latest/meta-data"}, TicketRequest{Title: "x"})
	if err == nil {
		t.Fatal("expected SSRF block for metadata address")
	}
}
