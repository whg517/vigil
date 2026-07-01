package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// === header 链路回归测试（resolver=nil，Resolve 走 header 分支）===

func TestRequireUser_EnforceTrue(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c *echo.Context) error { return c.String(200, "ok") }, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("enforce=true 无 header: got %d, want 401", rec.Code)
	}
}

func TestRequireUser_EnforceFalse(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c *echo.Context) error { return c.String(200, "ok") }, RequireUser(false, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("enforce=false 无 header: got %d, want 200", rec.Code)
	}
}

func TestRequireUser_ValidHeader(t *testing.T) {
	e := echo.New()
	var gotUID int
	var hasUID bool
	e.GET("/x", func(c *echo.Context) error {
		gotUID, hasUID = UserIDFromContext(c.Request().Context())
		return c.String(200, "ok")
	}, RequireUser(false, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Vigil-User-ID", "42")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if !hasUID || gotUID != 42 {
		t.Errorf("user not injected: hasUID=%v uid=%d", hasUID, gotUID)
	}
}

func TestRequireUser_InvalidHeader_EnforceTrue(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c *echo.Context) error { return c.String(200, "ok") }, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Vigil-User-ID", "abc")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("非法 header enforce=true: got %d, want 401", rec.Code)
	}
}

// === JWT 分支测试 ===

// doReq 辅助：用给定中间件挂路由，发请求，返回 recorder + 注入的 uid。
func doReq(t *testing.T, mw echo.MiddlewareFunc, setHeader func(*http.Request), enforceExpect int) (*httptest.ResponseRecorder, int, bool) {
	t.Helper()
	e := echo.New()
	var uid int
	var hasUID bool
	e.GET("/x", func(c *echo.Context) error {
		uid, hasUID = UserIDFromContext(c.Request().Context())
		return c.String(200, "ok")
	}, mw)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if setHeader != nil {
		setHeader(req)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != enforceExpect {
		t.Errorf("status got %d, want %d", rec.Code, enforceExpect)
	}
	return rec, uid, hasUID
}

func TestRequireUser_ValidJWT(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	r := NewIdentityResolver(s, nil, true)
	tok, _ := s.GenerateAccessToken(42, "alice")
	_, uid, hasUID := doReq(t, RequireUser(true, r), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	}, 200)
	if !hasUID || uid != 42 {
		t.Errorf("JWT user not injected: hasUID=%v uid=%d", hasUID, uid)
	}
}

func TestRequireUser_InvalidJWT_EnforceTrue(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	r := NewIdentityResolver(s, nil, true)
	doReq(t, RequireUser(true, r), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer invalid.token.here")
	}, 401)
}

func TestRequireUser_ExpiredJWT(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Millisecond, time.Millisecond)
	r := NewIdentityResolver(s, nil, true)
	tok, _ := s.GenerateAccessToken(1, "bob")
	time.Sleep(5 * time.Millisecond)
	_, _, hasUID := doReq(t, RequireUser(false, r), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	}, 200)
	if hasUID {
		t.Error("expired JWT injected uid, want none")
	}
}

// TestRequireUser_FallbackHeader 无 Bearer 时回退 X-Vigil-User-ID（兼容）。
func TestRequireUser_FallbackHeader(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	r := NewIdentityResolver(s, nil, true)
	_, uid, hasUID := doReq(t, RequireUser(true, r), func(r *http.Request) {
		r.Header.Set("X-Vigil-User-ID", "7")
	}, 200)
	if !hasUID || uid != 7 {
		t.Errorf("header fallback failed: hasUID=%v uid=%d", hasUID, uid)
	}
}

// TestRequireUser_JWTDoesNotFallbackOnBadToken 关键安全属性：无效 Bearer 不回退 header。
func TestRequireUser_JWTDoesNotFallbackOnBadToken(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	r := NewIdentityResolver(s, nil, true)
	_, _, hasUID := doReq(t, RequireUser(true, r), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer invalid.token")
		r.Header.Set("X-Vigil-User-ID", "99")
	}, 401)
	if hasUID {
		t.Error("bad Bearer fell back to header, want rejected")
	}
}

func TestRequireUser_RefreshTokenRejectedAsAccess(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	r := NewIdentityResolver(s, nil, true)
	refresh, _ := s.GenerateRefreshToken(5)
	_, _, hasUID := doReq(t, RequireUser(true, r), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+refresh)
	}, 401)
	if hasUID {
		t.Error("refresh token accepted as access, want rejected")
	}
}

// === API Key 分支测试 ===

// newAPIKeyTestClient 构造内存库 + 植入用户（id=1）和其 API Key。
func newAPIKeyTestClient(t *testing.T, plaintext string) (verifier *APIKeyVerifier, keyID int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:mw_apikey_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().
		SetUsername("alice").SetEmail("alice@vigil.local").
		SetPasswordHash(HashPassword("pw")).Save(ctx)
	k, _ := c.APIKey.Create().
		SetName("test-key").
		SetTokenHash(HashToken(plaintext)).
		SetPrefix(TokenPrefix(plaintext)).
		SetUserID(u.ID).
		Save(ctx)
	return NewAPIKeyVerifier(c), k.ID
}

// TestRequireUser_ValidAPIKey 有效 API Key 注入归属 user_id。
func TestRequireUser_ValidAPIKey(t *testing.T) {
	verifier, _ := newAPIKeyTestClient(t, "vgl_testkey123456")
	r := NewIdentityResolver(nil, verifier, true)
	_, uid, hasUID := doReq(t, RequireUser(true, r), func(req *http.Request) {
		req.Header.Set("X-Vigil-Key", "vgl_testkey123456")
	}, 200)
	if !hasUID || uid != 1 {
		t.Errorf("API Key user not injected: hasUID=%v uid=%d", hasUID, uid)
	}
}

// TestRequireUser_InvalidAPIKey 无效 API Key → 401。
func TestRequireUser_InvalidAPIKey(t *testing.T) {
	verifier, _ := newAPIKeyTestClient(t, "vgl_testkey123456")
	r := NewIdentityResolver(nil, verifier, true)
	doReq(t, RequireUser(true, r), func(req *http.Request) {
		req.Header.Set("X-Vigil-Key", "vgl_wrongkey")
	}, 401)
}

// TestRequireUser_APIKeyDoesNotFallbackOnBadKey 关键安全属性：无效 API Key 不回退 header。
func TestRequireUser_APIKeyDoesNotFallbackOnBadKey(t *testing.T) {
	verifier, _ := newAPIKeyTestClient(t, "vgl_testkey123456")
	r := NewIdentityResolver(nil, verifier, true)
	_, _, hasUID := doReq(t, RequireUser(true, r), func(req *http.Request) {
		req.Header.Set("X-Vigil-Key", "vgl_wrongkey")
		req.Header.Set("X-Vigil-User-ID", "99")
	}, 401)
	if hasUID {
		t.Error("bad API Key fell back to header, want rejected")
	}
}

// TestRequireUser_APIKeyFallbackHeader 无 X-Vigil-Key 时回退 header。
func TestRequireUser_APIKeyFallbackHeader(t *testing.T) {
	verifier, _ := newAPIKeyTestClient(t, "vgl_testkey123456")
	r := NewIdentityResolver(nil, verifier, true)
	_, uid, hasUID := doReq(t, RequireUser(true, r), func(req *http.Request) {
		req.Header.Set("X-Vigil-User-ID", "7")
	}, 200)
	if !hasUID || uid != 7 {
		t.Errorf("header fallback failed with apikey resolver: hasUID=%v uid=%d", hasUID, uid)
	}
}
