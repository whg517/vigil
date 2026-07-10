// config_template_test.go 集成配置模板端点契约测试（T6.2/M14.6 向导后端辅助）。
//
// 覆盖：按 type 返回单个模板；缺 type/all 返回全部；未知 type 返回 404。
package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func templateSetup(t *testing.T) *echo.Echo {
	t.Helper()
	c := newIntegrationTestClient(t, "integ_tpl_"+t.Name())
	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	return e
}

// TestConfigTemplate_ByType 指定 type 返回该类型模板（含 setup_hint）。
func TestConfigTemplate_ByType(t *testing.T) {
	e := templateSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/config-template?type=prometheus", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config-template prometheus: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var tpl configTemplate
	if err := json.Unmarshal(rec.Body.Bytes(), &tpl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tpl.Type != "prometheus" {
		t.Errorf("type: got %q, want prometheus", tpl.Type)
	}
	if tpl.SetupHint == "" {
		t.Error("setup_hint should be non-empty for prometheus")
	}
}

// TestConfigTemplate_All 缺 type 返回全部模板（覆盖 schema 枚举全集）。
func TestConfigTemplate_All(t *testing.T) {
	e := templateSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/config-template", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config-template all: got %d, want 200", rec.Code)
	}
	var resp struct {
		Templates []configTemplate `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 应覆盖 schema 枚举全集（7 种）。
	if len(resp.Templates) != 5 {
		t.Errorf("expected 5 templates (schema enum full set), got %d", len(resp.Templates))
	}
}

// TestConfigTemplate_Unknown 未知 type 返回 404。
func TestConfigTemplate_Unknown(t *testing.T) {
	e := templateSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/config-template?type=nonesuch", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("config-template unknown: got %d, want 404", rec.Code)
	}
}
