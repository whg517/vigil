// ssrf_test.go Runbook SSRF 防护测试（SEC-03 + FIX-2 DNS rebinding）。
package runbook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestValidateEndpoint_BlockedSchemes 非 http(s) scheme 应拒绝（静态校验，第一道关）。
func TestValidateEndpoint_BlockedSchemes(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"gopher://localhost:6379/_FLUSHALL",
		"dict://localhost:11211/stats",
		"data:text/plain,hello",
		"ftp://example.com/file",
	}
	for _, ep := range cases {
		if err := validateEndpoint(ep); err == nil {
			t.Errorf("scheme should be blocked: %s", ep)
		}
	}
}

// TestValidateEndpoint_Empty 空 endpoint 拒绝。
func TestValidateEndpoint_Empty(t *testing.T) {
	if err := validateEndpoint(""); err == nil {
		t.Error("empty endpoint should be rejected")
	}
}

// dialBlocked 用 allowPrivate=false 的 client 尝试 GET endpoint，
// 返回是否被 SSRF 防护拦截（连接失败 + 错误含 blocked）。
func dialBlocked(t *testing.T, endpoint string) bool {
	t.Helper()
	client := newHTTPClient(false)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return true // 构造请求失败也算拦
	}
	resp, err := client.Do(req)
	if err != nil {
		return strings.Contains(err.Error(), "blocked") || strings.Contains(err.Error(), "private")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return false // 连上了 = 未拦
}

// TestSSRF_LoopbackIPBlocked loopback IP 应被 dialer 拦截（连接时校验，防 rebinding）。
func TestSSRF_LoopbackIPBlocked(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
	}
	for _, ep := range cases {
		if !dialBlocked(t, ep) {
			t.Errorf("loopback should be blocked by dialer: %s", ep)
		}
	}
}

// TestSSRF_PrivateIPBlocked 私网 IP 应被 dialer 拦截。
func TestSSRF_PrivateIPBlocked(t *testing.T) {
	cases := []string{
		"http://10.0.0.1/",
		"http://172.16.0.1/",
		"http://192.168.1.1/",
	}
	for _, ep := range cases {
		if !dialBlocked(t, ep) {
			t.Errorf("private IP should be blocked by dialer: %s", ep)
		}
	}
}

// TestSSRF_CloudMetadataBlocked 云元数据地址必须被拦（SSRF 最常见目标）。
func TestSSRF_CloudMetadataBlocked(t *testing.T) {
	// 169.254.169.254 是 AWS/阿里云元数据地址
	if !dialBlocked(t, "http://169.254.169.254/latest/meta-data/") {
		t.Error("cloud metadata 169.254.169.254 must be blocked (critical SSRF target)")
	}
}

// TestSSRF_AllowPrivateOption allowPrivate=true 应放行私网（拨号可成功）。
// 用 httptest 启动 127.0.0.1 server，allowPrivate=true 应能连通。
func TestSSRF_AllowPrivateOption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newHTTPClient(true) // 放行私网
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowPrivate should permit loopback (httptest): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allowPrivate GET: got %d", resp.StatusCode)
	}
}

// TestSSRF_AllowPrivateStillBlocksBadScheme allowPrivate 仍拒绝非 http scheme（静态校验独立于 dialer）。
func TestSSRF_AllowPrivateStillBlocksBadScheme(t *testing.T) {
	v := &endpointValidator{allowPrivate: true}
	if err := v.validate("file:///etc/passwd"); err == nil {
		t.Error("allowPrivate should still block bad schemes")
	}
	// http URL 静态校验通过（IP 由 dialer 在 allowPrivate 下放行）
	if err := v.validate("http://127.0.0.1:8080/health"); err != nil {
		t.Errorf("allowPrivate http URL should pass static check: %v", err)
	}
}
