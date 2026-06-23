//go:build integration

package e2e

import (
	"net/http"
	"testing"
)

// TestSmoke_SetupHealthY 验证测试脚手架能启动完整实例且 /health 返回 200。
// 这是所有 e2e 用例的前提：若此用例失败，说明依赖未起或装配有问题，无需看后续用例。
func TestSmoke_SetupHealthy(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	resp, err := http.Get(env.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: got %d, want 200", resp.StatusCode)
	}
}

// TestSmoke_LoginAdmin 验证默认管理员种子存在，能登录拿 JWT。
// 后续需要鉴权的用例都依赖这一条链路打通。
func TestSmoke_LoginAdmin(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	token := env.Login(t)
	if token == "" {
		t.Fatal("login: empty token")
	}

	// 用 token 访问 /auth/me 验证 JWT 有效
	resp, err := http.DefaultClient.Do(env.AuthedJSON(t, http.MethodGet, token, "/auth/me", nil))
	if err != nil {
		t.Fatalf("GET /auth/me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/me: got %d, want 200 (token may be invalid)", resp.StatusCode)
	}
}
