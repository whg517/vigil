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

	"github.com/labstack/echo/v4"
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
