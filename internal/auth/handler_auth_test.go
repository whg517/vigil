package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newAuthTestClient 构造内存 sqlite client + 已植入测试用户。
// alice 密码 "pw"，bob 状态 disabled。
func newAuthTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:auth_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	_, _ = c.User.Create().
		SetUsername("alice").
		SetName("Alice").
		SetEmail("alice@vigil.local").
		SetPasswordHash(HashPassword("pw")).
		Save(ctx)
	_, _ = c.User.Create().
		SetUsername("bob").
		SetEmail("bob@vigil.local").
		SetPasswordHash(HashPassword("pw")).
		SetStatus(userStatusDisabled).
		Save(ctx)
	return c
}

const userStatusDisabled = "disabled"

// postJSON 辅助：构造 POST + JSON body 请求。
func postJSON(target string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

func newTestSigner1m() *JWTSigner {
	return NewJWTSigner("handler-test-secret-xxxxxxxxxxxxxx", time.Minute, time.Hour)
}

// TestLogin_Success 正确用户名密码 → 200 + 双 token + 用户信息。
func TestLogin_Success(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "pw"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp loginResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		t.Error("tokens empty")
	}
	if resp.User.Username != "alice" {
		t.Errorf("user=%+v", resp.User)
	}
	// access token 可被 signer 校验
	claims, err := s.ParseToken(resp.AccessToken)
	if err != nil {
		t.Errorf("access token invalid: %v", err)
	}
	if claims.TokenType != TokenTypeAccess {
		t.Errorf("token type=%q, want access", claims.TokenType)
	}
}

// TestLogin_WrongPassword 错误密码 → 401，错误信息不泄露用户是否存在。
func TestLogin_WrongPassword(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "wrong"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestLogin_UnknownUser 不存在的用户 → 401（与错误密码相同信息，防枚举）。
func TestLogin_UnknownUser(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "nobody", Password: "pw"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestLogin_DisabledUser 停用用户 → 403。
func TestLogin_DisabledUser(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "bob", Password: "pw"}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
}

// TestLogin_SignerNil signer 未配置 → 500（降级保护）。
func TestLogin_SignerNil(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, nil)
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "pw"}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
}

// TestRefresh_ValidRefresh 有效 refresh → 200 + 新 access。
func TestRefresh_ValidRefresh(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	refresh, _ := s.GenerateRefreshToken(1)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: refresh}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["access_token"] == "" {
		t.Error("access_token empty")
	}
}

// TestRefresh_WithAccessToken access token 不能用于 refresh → 401。
func TestRefresh_WithAccessToken(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	access, _ := s.GenerateAccessToken(1, "alice")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: access}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestRefresh_InvalidToken 无效 token → 401。
func TestRefresh_InvalidToken(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: "garbage"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestMe_Authenticated 已注入 uid → 200 + 用户信息（无 password_hash）。
func TestMe_Authenticated(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	// 模拟 RequireUser 已注入 uid（通过 X-Vigil-User-ID header，signer=nil 走 header 分支）
	e.GET("/api/v1/auth/me", h.me, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("X-Vigil-User-ID", "1") // alice 的 ID
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, `"username":"alice"`) {
		t.Errorf("response missing alice: %s", body)
	}
	// password_hash 不应泄露
	if contains(body, "password_hash") || contains(body, "PasswordHash") {
		t.Errorf("password_hash leaked in me response: %s", body)
	}
}

// TestMe_Unauthenticated 未注入 uid → 401。
func TestMe_Unauthenticated(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.GET("/api/v1/auth/me", h.me, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
