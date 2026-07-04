// handler_notfound_test.go 错误码归一测试（B25）：AI 端点对不存在的 id 应返回 404 not_found，
// 而非 500 internal。不注入 authorizer（checkAccess 放行），让请求落到引擎层触发 ent NotFound。
package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newBareHandler 无鉴权注入的 AI handler（checkAccess 降级放行），provider=nil。
func newBareHandler(t *testing.T) *echo.Echo {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:ai_notfound?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewHandler(NewDiagnoseEngine(c, nil))
	e := echo.New()
	h.Register(e.Group("/api/v1", auth.RequireUser(false, nil)))
	return e
}

// respCode 解析 ErrorResponse.code。
func respCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var r struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode error code: %v; body=%s", err, rec.Body.String())
	}
	return r.Code
}

// TestSimilar_NotFound_404 B25：对不存在的 incident id 查相似历史 → 404 not_found（而非 500）。
// similar 走 e.db.Incident.Get，不存在即 ent NotFound，经 errs.FailNotFound 归一为 404。
func TestSimilar_NotFound_404(t *testing.T) {
	e := newBareHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(999999)+"/similar", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("similar on missing incident: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if code := respCode(t, rec); code != "not_found" {
		t.Errorf("code: got %q, want not_found", code)
	}
}

// TestSimilarPostmortems_NotFound_404 B25：对不存在的 incident id 查相似复盘 → 404 not_found。
func TestSimilarPostmortems_NotFound_404(t *testing.T) {
	e := newBareHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(999999)+"/similar-postmortems", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("similar-postmortems on missing incident: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestResolveInsight_NotFound_404 B25：对不存在的 ai_insight id 改判 → 404 not_found（而非 500）。
func TestResolveInsight_NotFound_404(t *testing.T) {
	e := newBareHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-insights/"+strconv.Itoa(999999)+"/resolve", nil)
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("resolve on missing insight: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if code := respCode(t, rec); code != "not_found" {
		t.Errorf("code: got %q, want not_found", code)
	}
}
