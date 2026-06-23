//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestAuth_APIKeyAccess 验证 API Key 鉴权链路：
// 登录拿 JWT → 创建 API Key（返回一次性明文）→ 用 X-Vigil-Key 访问受保护 API。
func TestAuth_APIKeyAccess(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	// 1. 用 JWT 创建 API Key
	createBody := map[string]any{"name": "e2e-key", "scope": []string{"incident.read"}}
	req := env.AuthedJSON(t, http.MethodPost, token, "/api-keys", createBody)
	created, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create api-key: %v", err)
	}
	defer func() { _ = created.Body.Close() }()
	if created.StatusCode != http.StatusCreated && created.StatusCode != http.StatusOK {
		t.Fatalf("create api-key: got %d, want 2xx", created.StatusCode)
	}
	// apiKeyCreateResp 含一次性明文 token（字段名 token）
	var keyResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(created.Body).Decode(&keyResp); err != nil {
		t.Fatalf("decode api-key resp: %v", err)
	}
	if keyResp.Token == "" {
		t.Fatal("api-key create: empty token")
	}

	// 2. 用 X-Vigil-Key 头访问 /incidents（应通过身份解析）
	listReq, _ := http.NewRequest(http.MethodGet, env.APIURL("/incidents"), nil)
	listReq.Header.Set("X-Vigil-Key", keyResp.Token)
	resp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list with api-key: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("list with api-key: got %d, want 200", resp.StatusCode)
	}
}

// TestAuth_InvalidTokenRejected 验证无效 JWT 被拒（AUTH_ENABLED=true）。
func TestAuth_InvalidTokenRejected(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	req, _ := http.NewRequest(http.MethodGet, env.APIURL("/incidents"), nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list with invalid token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("list with invalid token: got 200, want non-200 (auth should reject)")
	}
}

// TestAuth_RefreshToken 验证 refresh token 能换新 access token。
func TestAuth_RefreshToken(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	// 登录拿 access + refresh
	loginBody := []byte(`{"username":"admin","password":"changeme"}`)
	req, _ := http.NewRequest(http.MethodPost, env.APIURL("/auth/login"), bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		t.Fatalf("decode login resp: %v", err)
	}
	_ = resp.Body.Close()
	if tokens.Refresh == "" {
		t.Fatal("login: empty refresh token")
	}

	// 用 refresh 换新 access
	refreshBody := []byte(`{"refresh_token":"` + tokens.Refresh + `"}`)
	req2, _ := http.NewRequest(http.MethodPost, env.APIURL("/auth/refresh"), bytes.NewReader(refreshBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("refresh: got %d, want 200", resp2.StatusCode)
	}
	var newTokens struct {
		Access string `json:"access_token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&newTokens); err != nil {
		t.Fatalf("decode refresh resp: %v", err)
	}
	if newTokens.Access == "" {
		t.Error("refresh: empty new access token")
	}
}
