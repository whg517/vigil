// ssrf_test.go Runbook SSRF 防护测试（SEC-03）。
package runbook

import (
	"strings"
	"testing"
)

// TestValidateEndpoint_BlockedSchemes 非 http(s) scheme 应拒绝。
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

// TestValidateEndpoint_LoopbackIP loopback IP 应拒绝。
func TestValidateEndpoint_LoopbackIP(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/",
		"http://localhost/", // localhost 通常解析到 127.0.0.1
		"http://[::1]/",
	}
	for _, ep := range cases {
		if err := validateEndpoint(ep); err == nil {
			t.Errorf("loopback should be blocked: %s", ep)
		}
	}
}

// TestValidateEndpoint_PrivateIP 私网 IP 应拒绝。
func TestValidateEndpoint_PrivateIP(t *testing.T) {
	cases := []string{
		"http://10.0.0.1/",
		"http://172.16.0.1/",
		"http://192.168.1.1/",
		"http://10.0.0.1:8080/api",
	}
	for _, ep := range cases {
		if err := validateEndpoint(ep); err == nil {
			t.Errorf("private IP should be blocked: %s", ep)
		}
	}
}

// TestValidateEndpoint_LinkLocal 链路本地（含云元数据）应拒绝。
func TestValidateEndpoint_LinkLocal(t *testing.T) {
	// 169.254.169.254 是 AWS/阿里云元数据地址（SSRF 最常见目标）
	if err := validateEndpoint("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("cloud metadata 169.254.169.254 must be blocked (SSRF)")
	}
}

// TestValidateEndpoint_AllowPrivateOption allowPrivate 开关应放行私网。
func TestValidateEndpoint_AllowPrivateOption(t *testing.T) {
	v := &endpointValidator{allowPrivate: true}
	// 放行私网（本地开发/同集群调用场景）
	if err := v.validate("http://127.0.0.1:8080/health"); err != nil {
		t.Errorf("allowPrivate should permit loopback, got %v", err)
	}
	// 但仍拒绝非 http scheme
	if err := v.validate("file:///etc/passwd"); err == nil {
		t.Error("allowPrivate should still block bad schemes")
	}
}

// TestValidateEndpoint_PublicAllowed 公网域名应放行（依赖 DNS）。
// 注：example.com 是保留测试域名，解析到公网 IP。
func TestValidateEndpoint_PublicAllowed(t *testing.T) {
	// 网络可能不可用，故只验证不返回"私网拒绝"类错误
	err := validateEndpoint("https://example.com/")
	if err != nil && strings.Contains(err.Error(), "private") {
		t.Errorf("public domain should not be blocked as private: %v", err)
	}
}
