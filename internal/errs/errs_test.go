// errs_test.go 统一错误模型测试（BE-03）。
package errs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/internal/httputil"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

// newCtx 构造测试用 *echo.Context（最小依赖，便于断言响应）。
// echo v5 的 NewContext 已返回 *Context，故直接用。
func newCtx(t *testing.T) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	return c, rec
}

// decode 解析响应体为 ErrorResponse。
func decode(t *testing.T, rec *httptest.ResponseRecorder) httputil.ErrorResponse {
	t.Helper()
	var r httputil.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return r
}

func TestBadRequest(t *testing.T) {
	c, rec := newCtx(t)
	if err := BadRequest(c, "invalid id"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	r := decode(t, rec)
	if r.Error != "invalid id" {
		t.Errorf("msg: got %q", r.Error)
	}
	if r.Code != CodeInvalidArgument {
		t.Errorf("code: got %q", r.Code)
	}
}

func TestNotFound(t *testing.T) {
	c, rec := newCtx(t)
	if err := NotFound(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d", rec.Code)
	}
	r := decode(t, rec)
	if r.Code != CodeNotFound {
		t.Errorf("code: got %q", r.Code)
	}
	// 带 msg 覆盖
	c2, rec2 := newCtx(t)
	_ = NotFound(c2, "incident not found")
	if decode(t, rec2).Error != "incident not found" {
		t.Error("custom msg not applied")
	}
}

func TestForbidden(t *testing.T) {
	c, rec := newCtx(t)
	_ = Forbidden(c, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d", rec.Code)
	}
	if decode(t, rec).Code != CodePermissionDenied {
		t.Error("wrong code")
	}
}

func TestRateLimited(t *testing.T) {
	c, rec := newCtx(t)
	_ = RateLimited(c, "too many", 60)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status: got %d", rec.Code)
	}
	r := decode(t, rec)
	if r.Code != CodeRateLimited {
		t.Error("wrong code")
	}
	// Details 应含 retry_after_seconds
	d, ok := r.Details.(map[string]any)
	if !ok {
		t.Fatal("details not a map")
	}
	if d["retry_after_seconds"] == nil {
		t.Error("retry_after_seconds missing")
	}
}

// TestInternal_NoLeak 验证 Internal 不泄露底层 err.Error()，前端只见通用 message。
func TestInternal_NoLeak(t *testing.T) {
	c, rec := newCtx(t)
	// 模拟含敏感信息的底层错误
	sensitive := errors.New("pq: relation \"users_secret\" does not exist (SQLSTATE 42P01)")
	_ = Internal(c, nil, sensitive)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d", rec.Code)
	}
	r := decode(t, rec)
	if r.Error != "internal error" {
		t.Errorf("should return generic message, got %q (leaks details!)", r.Error)
	}
	if r.Code != CodeInternal {
		t.Errorf("code: got %q", r.Code)
	}
}

// TestInternal_GlobalLoggerFallback 验证 SetLogger 注入后，Internal(nil) 不 panic 且
// 走全局 logger 路径（FIX-3：消除 handler 传 nil 导致不记录的回归）。
func TestInternal_GlobalLoggerFallback(t *testing.T) {
	prev := globalLogger
	t.Cleanup(func() { globalLogger = prev })
	SetLogger(zap.NewNop()) // 模拟装配期注入
	c, _ := newCtx(t)
	// Internal 传 nil 应回退用全局 logger，不 panic
	if err := Internal(c, nil, errors.New("db down")); err != nil {
		t.Fatalf("Internal with global fallback: %v", err)
	}
}

// TestSetLogger 验证 SetLogger 设置全局 logger（装配期调用）。
func TestSetLogger(t *testing.T) {
	prev := globalLogger
	t.Cleanup(func() { globalLogger = prev })
	SetLogger(zap.NewNop())
	if globalLogger == nil {
		t.Error("SetLogger should set globalLogger")
	}
}

func TestFailNotFound_EntNotFound(t *testing.T) {
	c, rec := newCtx(t)
	err := errors.New("ent: incident not found")
	_ = FailNotFound(c, nil, err, "incident")
	if rec.Code != http.StatusNotFound {
		t.Errorf("ent not found should map to 404, got %d", rec.Code)
	}
	r := decode(t, rec)
	if r.Error != "incident not found" {
		t.Errorf("msg: got %q", r.Error)
	}
}

func TestFailNotFound_SqlNoRows(t *testing.T) {
	c, rec := newCtx(t)
	err := errors.New("sql: no rows in result set")
	_ = FailNotFound(c, nil, err, "runbook")
	if rec.Code != http.StatusNotFound {
		t.Errorf("sql no rows should map to 404, got %d", rec.Code)
	}
}

func TestFailNotFound_OtherErrorMapsInternal(t *testing.T) {
	c, rec := newCtx(t)
	err := errors.New("connection refused")
	_ = FailNotFound(c, nil, err, "incident")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("non-not-found err should map to 500, got %d", rec.Code)
	}
	if decode(t, rec).Error != "internal error" {
		t.Error("should be generic internal message")
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("ent: user not found"), true},
		{errors.New("sql: no rows in result set"), true},
		{errors.New("connection refused"), false},
		{errors.New("permission denied"), false},
	}
	for _, c := range cases {
		if got := isNotFound(c.err); got != c.want {
			t.Errorf("isNotFound(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestFailConstraint_DuplicateKey FIX-B：重复键约束冲突应返回 409 Conflict。
func TestFailConstraint_DuplicateKey(t *testing.T) {
	cases := []string{
		`pq: duplicate key value violates unique constraint "teams_slug_key" (23505)`,
		`ent: constraint failed: pq: duplicate key value violates unique constraint "services_slug_key"`,
		`UNIQUE constraint failed: users.username`,
		`duplicate key value violates unique constraint "roles_name_key"`,
	}
	for _, errStr := range cases {
		c, rec := newCtx(t)
		_ = FailConstraint(c, nil, errors.New(errStr), "team", "team slug already exists")
		if rec.Code != http.StatusConflict {
			t.Errorf("constraint error should map to 409, got %d for %q", rec.Code, errStr)
		}
		r := decode(t, rec)
		if r.Code != CodeAlreadyExists {
			t.Errorf("code: got %q, want %q", r.Code, CodeAlreadyExists)
		}
		if r.Error != "team slug already exists" {
			t.Errorf("msg: got %q", r.Error)
		}
	}
}

// TestFailConstraint_OtherErrorMapsInternal 非约束错误应走 500 Internal（不误判 409）。
func TestFailConstraint_OtherErrorMapsInternal(t *testing.T) {
	c, rec := newCtx(t)
	_ = FailConstraint(c, nil, errors.New("connection refused"), "team", "msg")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("non-constraint err should map to 500, got %d", rec.Code)
	}
	if decode(t, rec).Error != "internal error" {
		t.Error("should be generic internal, not leak details")
	}
}
