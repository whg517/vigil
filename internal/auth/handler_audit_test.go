package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v4"
	_ "github.com/mattn/go-sqlite3"
)

// 植入若干审计日志供查询测试。
func seedAuditLogs(t *testing.T) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	r := NewAuditRecorder(c)
	_ = r.Record(ctx, AuditEntry{ActorUserID: 1, Action: "role.create", ResourceType: "role", ResourceID: 1, Result: AuditResultSuccess})
	_ = r.Record(ctx, AuditEntry{ActorUserID: 2, Action: "role.delete", ResourceType: "role", ResourceID: 2, Result: AuditResultSuccess})
	_ = r.Record(ctx, AuditEntry{ActorUserID: 1, Action: "auth.login", ResourceType: "user", Result: AuditResultFailed, ActorName: "attacker"})
	_ = r.Record(ctx, AuditEntry{ActorUserID: 3, Action: "apikey.create", ResourceType: "api_key", ResourceID: 5, Result: AuditResultSuccess})
}

func TestAuditHandler_ListAll(t *testing.T) {
	seedAuditLogs(t)
	// 用同一个内存库 DSN 重新打开（cache=shared 共享数据）
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs", h.list, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	// 默认倒序，应返回全部 4 条
	if !contains(rec.Body.String(), `"total":4`) {
		t.Errorf("total not 4: %s", rec.Body.String())
	}
}

func TestAuditHandler_FilterByAction(t *testing.T) {
	seedAuditLogs(t)
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs", h.list, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?action=auth.login", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !contains(rec.Body.String(), `"total":1`) {
		t.Errorf("filtered total not 1: %s", rec.Body.String())
	}
}

func TestAuditHandler_FilterByActor(t *testing.T) {
	seedAuditLogs(t)
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs", h.list, RequireUser(true, nil))

	// actor_user_id=1 有 2 条（role.create + auth.login）
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?actor_user_id=1", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if !contains(rec.Body.String(), `"total":2`) {
		t.Errorf("actor filter total not 2: %s", rec.Body.String())
	}
}

func TestAuditHandler_Pagination(t *testing.T) {
	seedAuditLogs(t)
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs", h.list, RequireUser(true, nil))

	// limit=2 offset=0，返回 2 条但 total=4
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=2&offset=0", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !contains(body, `"total":4`) {
		t.Errorf("total not 4: %s", body)
	}
	if !contains(body, `"limit":2`) {
		t.Errorf("limit not echoed: %s", body)
	}
}
