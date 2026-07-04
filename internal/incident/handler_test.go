package incident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// TestHandler_AckActorFromContext 验证 ack 的操作人来自鉴权 context（X-Vigil-User-ID），
// 而非请求 body —— 对应 CLAUDE.md 边界：不绕过 RBAC、防伪造 actor。
func TestHandler_AckActorFromContext(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	team, err := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	user, err := c.User.Create().SetUsername("zhangsan").SetEmail("zs@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").SetTitle("5xx").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}

	svc := NewService(c, timeline.NewRecorder(c), nil)
	h := NewHandler(c, svc)

	e := echo.New()
	// 复刻线上装配：v1 组挂 RequireUser(false) 做身份解析（渐进式鉴权阶段）
	v1 := e.Group("/api/v1", auth.RequireUser(false, nil))
	h.Register(v1)

	// 关键：body 不带 actor_id，仅靠 header X-Vigil-User-ID 标识身份。
	// 修复前 actor 恒为 0（assignee 不设置）；修复后应取自 context 的 user.ID。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(inc.ID)+"/ack", nil)
	req.Header.Set("X-Vigil-User-ID", itoa(user.ID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ack: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got ent.Incident
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != incident.StatusAcked {
		t.Errorf("status: got %s, want acked", got.Status)
	}
	a, _ := c.Incident.Get(ctx, inc.ID)
	assignee, _ := a.QueryAssignee().Only(ctx)
	if assignee == nil || assignee.ID != user.ID {
		t.Errorf("assignee should be %d from context, got %v", user.ID, assignee)
	}
}

// TestHandler_AckNoUser_NoActor 无身份 header 时 actor=0（系统/匿名），不报错。
func TestHandler_AckNoUser_NoActor(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handler_test2?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	team, err := c.Team.Create().SetName("T").SetSlug("t").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").SetTitle("x").SetSeverity(incident.SeverityWarning).
		SetStatus(incident.StatusTriggered).SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}

	svc := NewService(c, timeline.NewRecorder(c), nil)
	h := NewHandler(c, svc)
	e := echo.New()
	h.Register(e.Group("/api/v1", auth.RequireUser(false, nil)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(inc.ID)+"/ack", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack without user: got %d, want 200", rec.Code)
	}
}

// closeTestHandler 建库 + 一个指定状态的 incident + 挂好路由的 echo，返回 (client, echo, incID)。
func closeTestHandler(t *testing.T, dbName string, status incident.Status) (*ent.Client, *echo.Echo, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, err := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").SetTitle("5xx").SetSeverity(incident.SeverityCritical).
		SetStatus(status).SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	svc := NewService(c, timeline.NewRecorder(c), nil)
	h := NewHandler(c, svc)
	e := echo.New()
	h.Register(e.Group("/api/v1", auth.RequireUser(false, nil)))
	return c, e, inc.ID
}

// TestHandler_Close_FromResolved resolved incident close 成功，返回 200 + status=closed。
func TestHandler_Close_FromResolved(t *testing.T) {
	c, e, incID := closeTestHandler(t, "close_handler_ok", incident.StatusResolved)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(incID)+"/close", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got ent.Incident
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != incident.StatusClosed {
		t.Errorf("status: got %s, want closed", got.Status)
	}
	// 落库确认
	after, _ := c.Incident.Get(context.Background(), incID)
	if after.Status != incident.StatusClosed {
		t.Errorf("persisted status: got %s, want closed", after.Status)
	}
}

// TestHandler_Close_FromTriggered 非 resolved（triggered）close 返回 400 failed_precondition。
func TestHandler_Close_FromTriggered(t *testing.T) {
	_, e, incID := closeTestHandler(t, "close_handler_bad", incident.StatusTriggered)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(incID)+"/close", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("close from triggered: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandler_Close_Idempotent 已 closed 再 close 幂等返回 200（不当 400 失败）。
func TestHandler_Close_Idempotent(t *testing.T) {
	_, e, incID := closeTestHandler(t, "close_handler_idem", incident.StatusClosed)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(incID)+"/close", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("close already-closed: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got ent.Incident
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != incident.StatusClosed {
		t.Errorf("status: got %s, want closed", got.Status)
	}
}

// TestHandler_Ack_InvalidTransition_400 B25：对已 resolved 单再 ack（状态机非法流转）→ 400 failed_precondition。
func TestHandler_Ack_InvalidTransition_400(t *testing.T) {
	_, e, incID := closeTestHandler(t, "ack_invalid_transition", incident.StatusResolved)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(incID)+"/ack", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ack from resolved: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec); code != "failed_precondition" {
		t.Errorf("code: got %q, want failed_precondition", code)
	}
}

// TestHandler_Ack_NotFound_404 B25：对不存在的 incident id 做处置 → 404 not_found（而非 400）。
func TestHandler_Ack_NotFound_404(t *testing.T) {
	_, e, incID := closeTestHandler(t, "ack_not_found", incident.StatusTriggered)
	missing := incID + 9999 // 保证不存在
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(missing)+"/ack", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ack missing id: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec); code != "not_found" {
		t.Errorf("code: got %q, want not_found", code)
	}
}

// TestHandler_Resolve_NotFound_404 B25：resolve 不存在的 id → 404 not_found。
func TestHandler_Resolve_NotFound_404(t *testing.T) {
	_, e, incID := closeTestHandler(t, "resolve_not_found", incident.StatusTriggered)
	missing := incID + 9999
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(missing)+"/resolve", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("resolve missing id: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// errCode 解析 ErrorResponse.code（用于断言归一后的机器可读错误码）。
func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var r struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode error code: %v; body=%s", err, rec.Body.String())
	}
	return r.Code
}

// itoa 简单整数转字符串（避免引入 strconv 仅为此）。
func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
