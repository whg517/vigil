package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/internal/web"
	"github.com/labstack/echo/v5"
)

// TestStatic_ServesIndex 根路径 / 返回前端 index.html（非 404，内容是 SPA 入口）。
func TestStatic_ServesIndex(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	// 仅当 embed 了真实产物时才验证内容；只有 .gitkeep 占位时 index.html 不存在，
	// 此时 StaticDirectoryHandler 返回 ErrNotFound → fallback 到 index.html 也 404。
	if !distHasIndex() {
		t.Skip("web/dist 无 index.html（仅占位），跳过静态 serve 内容断言")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 状态 %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<div id=\"root\">") && !strings.Contains(body, "root") {
		t.Errorf("GET / 未返回 SPA 入口: %s", body)
	}
}

// TestStatic_SPAFallback 非文件的前端路由（如 /incidents/123）回退到 index.html，
// 而非 404（SPA history mode 必需）。
func TestStatic_SPAFallback(t *testing.T) {
	s := newTestServer(t)
	if !distHasIndex() {
		t.Skip("web/dist 无 index.html（仅占位），跳过 SPA fallback 断言")
	}

	req := httptest.NewRequest(http.MethodGet, "/incidents/123", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	// 前端路由 /incidents/123 不存在于文件系统，应 fallback 到 index.html（200）
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /incidents/123 状态 %d, want 200（SPA fallback）; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestStatic_DoesNotShadowAPI 静态 catch-all /* 不吞掉 /health 等具体路由
// （路由优先级 static > param > any 保证）。
func TestStatic_DoesNotShadowAPI(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health 被 static 吞掉: 状态 %d, want 200（应命中 health handler）",
			rec.Code)
	}
	// health 返回 JSON 含 checks 字段，证明走的是 health 而非 index.html
	if !strings.Contains(rec.Body.String(), "checks") {
		t.Errorf("GET /health 未返回 health 响应: %s", rec.Body.String())
	}
}

// TestStatic_ServesAsset 静态资源（如 favicon.svg）能正确返回。
func TestStatic_ServesAsset(t *testing.T) {
	s := newTestServer(t)
	if !distHasFile("favicon.svg") {
		t.Skip("web/dist 无 favicon.svg，跳过静态资源断言")
	}

	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /favicon.svg 状态 %d, want 200", rec.Code)
	}
}

// distHasIndex 判断 embed 的 dist 是否含 index.html（区分真实产物 vs 仅占位）。
func distHasIndex() bool { return distHasFile("index.html") }

// distHasFile 判断 embed 的 dist 根下是否存在某文件。
func distHasFile(name string) bool {
	entries, err := fs.ReadDir(web.DistFS, "dist")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && e.Name() == name {
			return true
		}
	}
	return false
}

// 确保 echo 包引用（与 server_test.go 的 var _ = echo.New 对齐，避免在仅编译本文件时未使用）。
var _ = echo.New
