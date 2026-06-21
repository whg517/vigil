package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// === 原 header 链路回归测试（signer=nil，保持旧行为）===

// TestRequireUser_EnforceTrue 验证 enforce=true 时无 header 返回 401。
func TestRequireUser_EnforceTrue(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil) // 无 X-Vigil-User-ID
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("enforce=true 无 header: got %d, want 401", rec.Code)
	}
}

// TestRequireUser_EnforceFalse 验证 enforce=false 时无 header 放行。
func TestRequireUser_EnforceFalse(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(false, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil) // 无 header
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("enforce=false 无 header: got %d, want 200", rec.Code)
	}
}

// TestRequireUser_ValidHeader 验证有效 header 注入用户到 context。
func TestRequireUser_ValidHeader(t *testing.T) {
	e := echo.New()
	var gotUID int
	var hasUID bool
	e.GET("/x", func(c echo.Context) error {
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

// TestRequireUser_InvalidHeader_EnforceTrue 验证非法 header enforce=true 时 401。
func TestRequireUser_InvalidHeader_EnforceTrue(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Vigil-User-ID", "abc") // 非数字
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("非法 header enforce=true: got %d, want 401", rec.Code)
	}
}

// === JWT 分支测试 ===

// doReq 辅助：用给定中间件挂路由，发请求，返回 recorder + 是否注入了 uid。
func doReq(t *testing.T, mw echo.MiddlewareFunc, setHeader func(*http.Request), enforceExpect int) (*httptest.ResponseRecorder, int, bool) {
	t.Helper()
	e := echo.New()
	var uid int
	var hasUID bool
	e.GET("/x", func(c echo.Context) error {
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

// TestRequireUser_ValidJWT 有效 JWT 注入正确 userID。
func TestRequireUser_ValidJWT(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	tok, _ := s.GenerateAccessToken(42, "alice")
	_, uid, hasUID := doReq(t, RequireUser(true, s), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	}, 200)
	if !hasUID || uid != 42 {
		t.Errorf("JWT user not injected: hasUID=%v uid=%d", hasUID, uid)
	}
}

// TestRequireUser_InvalidJWT_EnforceTrue 篡改的 JWT + enforce=true → 401。
func TestRequireUser_InvalidJWT_EnforceTrue(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	doReq(t, RequireUser(true, s), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer invalid.token.here")
	}, 401)
}

// TestRequireUser_ExpiredJWT 过期 JWT → 无注入（enforce=false 为 200 但无 uid）。
func TestRequireUser_ExpiredJWT(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Millisecond, time.Millisecond)
	tok, _ := s.GenerateAccessToken(1, "bob")
	time.Sleep(5 * time.Millisecond)
	_, _, hasUID := doReq(t, RequireUser(false, s), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	}, 200)
	if hasUID {
		t.Error("expired JWT injected uid, want none")
	}
}

// TestRequireUser_FallbackHeader 无 Bearer 时回退 X-Vigil-User-ID（兼容）。
func TestRequireUser_FallbackHeader(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	_, uid, hasUID := doReq(t, RequireUser(true, s), func(r *http.Request) {
		r.Header.Set("X-Vigil-User-ID", "7")
	}, 200)
	if !hasUID || uid != 7 {
		t.Errorf("header fallback failed: hasUID=%v uid=%d", hasUID, uid)
	}
}

// TestRequireUser_JWTDoesNotFallbackOnBadToken
// 关键安全属性：带了无效 Bearer 时不回退 header（避免伪造降级）。
// 即使同时带了有效 X-Vigil-User-ID，也应判无身份。
func TestRequireUser_JWTDoesNotFallbackOnBadToken(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	_, _, hasUID := doReq(t, RequireUser(true, s), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer invalid.token")
		r.Header.Set("X-Vigil-User-ID", "99") // 即使有效 header 也应被忽略
	}, 401)
	if hasUID {
		t.Error("bad Bearer fell back to header, want rejected")
	}
}

// TestRequireUser_RefreshTokenRejectedAsAccess refresh token 不能当 access 用。
func TestRequireUser_RefreshTokenRejectedAsAccess(t *testing.T) {
	s := NewJWTSigner("mw-test-secret-xxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	refresh, _ := s.GenerateRefreshToken(5)
	// refresh 的 token_type != access，中间件 resolveUserID 要求 TokenTypeAccess
	_, _, hasUID := doReq(t, RequireUser(true, s), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+refresh)
	}, 401)
	if hasUID {
		t.Error("refresh token accepted as access, want rejected")
	}
}
