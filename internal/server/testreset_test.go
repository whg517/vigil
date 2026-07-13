// testreset_test.go 测试 reset 端点注册门控（SEC-02 修订）。
//
// 核心断言：/api/v1/__test__/reset 默认**不注册**——须 VIGIL_TEST_ENDPOINTS_ENABLED
// 显式开启，且生产环境即使开启也强制不注册。
package server

import "testing"

const testResetPath = "/api/v1/__test__/reset"

// hasRoute 检查 echo 路由表中是否存在指定 method+path 的路由。
func hasRoute(s *Server, method, path string) bool {
	for _, r := range s.echo.Router().Routes() {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// TestRegisterTestResetIfEnabled_DefaultOff 验证默认配置（开关未开）下端点不注册。
func TestRegisterTestResetIfEnabled_DefaultOff(t *testing.T) {
	s := newTestServer(t) // cfg 零值：TestEndpoints.Enabled=false（与 envconfig default 一致）
	if s.registerTestResetIfEnabled() {
		t.Fatal("test reset endpoint must NOT register by default")
	}
	if hasRoute(s, "POST", testResetPath) {
		t.Fatalf("route %s must be absent by default", testResetPath)
	}
}

// TestRegisterTestResetIfEnabled_ExplicitOn 验证显式开启且非生产时端点注册（e2e 场景）。
func TestRegisterTestResetIfEnabled_ExplicitOn(t *testing.T) {
	s := newTestServer(t)
	s.cfg.App.Env = "development"
	s.cfg.TestEndpoints.Enabled = true
	if !s.registerTestResetIfEnabled() {
		t.Fatal("explicitly enabled test reset endpoint should register in development")
	}
	if !hasRoute(s, "POST", testResetPath) {
		t.Fatalf("route %s should exist after explicit enable", testResetPath)
	}
}

// TestRegisterTestResetIfEnabled_ProductionForcedOff 验证生产环境显式开启也不注册（双保险）。
func TestRegisterTestResetIfEnabled_ProductionForcedOff(t *testing.T) {
	s := newTestServer(t)
	s.cfg.App.Env = "production"
	s.cfg.TestEndpoints.Enabled = true
	if s.registerTestResetIfEnabled() {
		t.Fatal("production must force test reset endpoint off even when enabled")
	}
	if hasRoute(s, "POST", testResetPath) {
		t.Fatalf("route %s must be absent in production", testResetPath)
	}
}
