package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestRequireUser_EnforceTrue 验证 enforce=true 时无 header 返回 401。
func TestRequireUser_EnforceTrue(t *testing.T) {
	e := echo.New()
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(true))
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
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(false))
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
	}, RequireUser(false))
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
	e.GET("/x", func(c echo.Context) error { return c.String(200, "ok") }, RequireUser(true))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Vigil-User-ID", "abc") // 非数字
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("非法 header enforce=true: got %d, want 401", rec.Code)
	}
}
