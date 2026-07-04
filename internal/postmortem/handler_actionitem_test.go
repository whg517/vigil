// handler_actionitem_test.go ActionItem due_date 入参契约测试（T2.5 / M14）。
//
// schema 有 due_date 字段（Optional().Nillable()），此前 create/update 请求体不收。
// 本测试锁定：POST action-items 收 due_date 并落库；PATCH 可改 due_date。
// 无 authz 注入（checkAccess 降级放行），聚焦请求体解析与落库。
package postmortem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func aiSetup(t *testing.T) (*echo.Echo, *ent.Client, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:pm_ai_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	inc := c.Incident.Create().SetNumber("INC-1").SetTitle("t").
		SetSeverity("critical").SetStatus("resolved").SaveX(ctx)
	pm := c.Postmortem.Create().SetIncidentID(inc.ID).SetStatus("draft").
		SetGeneratedBy("human").SetSections(map[string]any{}).SaveX(ctx)

	h := NewHandler(c, NewEngine(c, nil)) // 无 authz：checkAccess 降级放行
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	return e, c, pm.ID
}

func doAIReq(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestAddActionItem_DueDate POST action-items 收 due_date 并落库。
func TestAddActionItem_DueDate(t *testing.T) {
	e, c, pmID := aiSetup(t)
	due := "2026-08-01T00:00:00Z"
	body := `{"description":"补监控","due_date":"` + due + `"}`
	rec := doAIReq(e, http.MethodPost, "/api/v1/postmortems/"+strconv.Itoa(pmID)+"/action-items", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add action item: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      int        `json:"id"`
		DueDate *time.Time `json:"due_date"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DueDate == nil {
		t.Fatal("due_date not persisted (nil in response)")
	}
	want, _ := time.Parse(time.RFC3339, due)
	if !resp.DueDate.Equal(want) {
		t.Errorf("due_date: got %v, want %v", resp.DueDate, want)
	}
	// 回读 db 确认落库。
	got := c.ActionItem.GetX(t.Context(), resp.ID)
	if got.DueDate == nil || !got.DueDate.Equal(want) {
		t.Errorf("due_date in db: got %v, want %v", got.DueDate, want)
	}
}

// TestUpdateActionItem_DueDate PATCH 可改 due_date。
func TestUpdateActionItem_DueDate(t *testing.T) {
	e, c, pmID := aiSetup(t)
	ai := c.ActionItem.Create().SetDescription("x").SetPostmortemID(pmID).SaveX(t.Context())
	due := "2026-09-15T12:00:00Z"
	rec := doAIReq(e, http.MethodPatch, "/api/v1/action-items/"+strconv.Itoa(ai.ID),
		`{"due_date":"`+due+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update action item: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	want, _ := time.Parse(time.RFC3339, due)
	got := c.ActionItem.GetX(t.Context(), ai.ID)
	if got.DueDate == nil || !got.DueDate.Equal(want) {
		t.Errorf("due_date after update: got %v, want %v", got.DueDate, want)
	}
}
