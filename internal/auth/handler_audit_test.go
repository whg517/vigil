package auth

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
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

// TestAuditHandler_FilterByTime 验证 from/to 时间区间筛选（C21）。
// 直接以受控 created_at 植入 3 条日志（昨天/今天/明天），断言各区间只返回命中的条目。
func TestAuditHandler_FilterByTime(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:audit_time_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	// 截到整秒：RFC3339 边界只到秒，带纳秒的 created_at 会让 to=now（LTE）漏掉"今天"这条。
	now := time.Now().UTC().Truncate(time.Second)
	yesterday := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)
	for _, ts := range []time.Time{yesterday, now, tomorrow} {
		if err := c.AuditLog.Create().
			SetAction("x").SetResourceType("t").SetCreatedAt(ts).
			Exec(ctx); err != nil {
			t.Fatalf("seed audit at %v: %v", ts, err)
		}
	}

	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs", h.list, RequireUser(true, nil))

	call := func(query string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?"+query, nil)
		req.Header.Set("X-Vigil-User-ID", "1")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// from=now（含）→ 排除昨天，命中今天+明天 = 2 条（RFC3339 边界）。
	if body := call("from=" + now.Format(time.RFC3339)); !contains(body, `"total":2`) {
		t.Errorf("from filter total not 2: %s", body)
	}
	// to=now（含）→ 排除明天，命中昨天+今天 = 2 条。
	if body := call("to=" + now.Format(time.RFC3339)); !contains(body, `"total":2`) {
		t.Errorf("to filter total not 2: %s", body)
	}
	// from+to 夹取 now±1h → 仅命中今天 1 条。
	q := "from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339)
	if body := call(q); !contains(body, `"total":1`) {
		t.Errorf("from+to window total not 1: %s", body)
	}
	// unix 秒格式同样支持（to=昨天+1h → 仅命中昨天 1 条）。
	if body := call("to=" + strconv.FormatInt(yesterday.Add(time.Hour).Unix(), 10)); !contains(body, `"total":1`) {
		t.Errorf("unix to filter total not 1: %s", body)
	}
}

// TestAuditHandler_Export 验证 CSV 导出：header 行、数据行数、列内容、detail 压平与转义。
func TestAuditHandler_Export(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:audit_export_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	r := NewAuditRecorder(c)
	_ = r.Record(ctx, AuditEntry{ActorUserID: 1, ActorName: "alice", Action: "role.create", ResourceType: "role", ResourceID: 1, ResourceName: "on-call", Result: AuditResultSuccess, IP: "10.0.0.1", UserAgent: "curl/8"})
	// detail 带逗号/引号，验证 CSV 转义正确（csv.Writer 应给该单元格加引号）。
	_ = r.Record(ctx, AuditEntry{ActorUserID: 2, Action: "auth.login", ResourceType: "user", Result: AuditResultFailed, Detail: map[string]any{"reason": "bad, \"password\""}})

	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs/export", h.exportAuditLogs, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/export", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !contains(ct, "text/csv") {
		t.Errorf("content-type not csv: %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !contains(cd, "attachment") || !contains(cd, "audit-logs_") {
		t.Errorf("content-disposition unexpected: %q", cd)
	}

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	// header + 2 数据行
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (header+2), got %d: %v", len(rows), rows)
	}
	wantHeader := []string{"created_at", "actor_user_id", "actor_name", "action", "resource_type", "resource_id", "resource_name", "result", "ip", "user_agent", "detail"}
	if len(rows[0]) != len(wantHeader) {
		t.Fatalf("header len %d != %d", len(rows[0]), len(wantHeader))
	}
	for i, col := range wantHeader {
		if rows[0][i] != col {
			t.Errorf("header[%d]=%q want %q", i, rows[0][i], col)
		}
	}
	// 倒序：最新（auth.login）在前。断言 detail 列包含转义后的原始内容（csv 解析后还原）。
	if rows[1][3] != "auth.login" {
		t.Errorf("row1 action=%q want auth.login (desc order)", rows[1][3])
	}
	// detail 列是 JSON 压平串；csv 解析后应还原为 {"reason":"bad, \"password\""}。
	if !contains(rows[1][10], `"reason"`) || !contains(rows[1][10], `bad, `) {
		t.Errorf("row1 detail not preserved: %q", rows[1][10])
	}
	// 第二条数据行（role.create）字段抽查。
	if rows[2][2] != "alice" || rows[2][6] != "on-call" || rows[2][8] != "10.0.0.1" {
		t.Errorf("row2 fields unexpected: %v", rows[2])
	}
}

// TestAuditHandler_ExportFilter 验证导出复用 list 同一套筛选（按 action 过滤）。
func TestAuditHandler_ExportFilter(t *testing.T) {
	seedAuditLogs(t)
	c := enttest.Open(t, "sqlite3", "file:audit_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewAuditHandler(c)
	e := echo.New()
	e.GET("/api/v1/audit-logs/export", h.exportAuditLogs, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs/export?action=auth.login", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	// header + 1 命中行（seedAuditLogs 中 auth.login 只有 1 条）
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (header+1 filtered), got %d: %v", len(rows), rows)
	}
	if rows[1][3] != "auth.login" {
		t.Errorf("filtered row action=%q want auth.login", rows[1][3])
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
