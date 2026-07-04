package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/ent/enttest"
	entuser "github.com/kevin/vigil/ent/user"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newUserHandlerTest 起一个 active 用户 + 装好审计记录器的 UserHandler。
// actorID 作为 X-Vigil-User-ID 注入（操作者），targetID 为被操作用户。
func newUserHandlerTest(t *testing.T) (*ent.Client, *UserHandler, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:user_audit_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	u, err := c.User.Create().
		SetUsername("target").SetEmail("t@x.com").
		SetStatus(entuser.StatusActive).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := NewUserHandler(c)
	h.SetAuditRecorder(NewAuditRecorder(c)) // 不注入 authz → 退化为仅 user.update 门禁，专测审计
	return c, h, u.ID
}

// patchStatus 走完整 echo 链路 PATCH /users/:id，返回响应码。
func patchStatus(t *testing.T, h *UserHandler, actorID, targetID int, status string) int {
	t.Helper()
	e := echo.New()
	e.PATCH("/api/v1/users/:id", h.updateUser, RequireUser(true, nil))
	body := strings.NewReader(`{"status":"` + status + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+strconv.Itoa(targetID), body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(actorID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code
}

// TestUpdateUser_DisableAudited 停用用户 → 落一条 user.disable 审计（C21）。
func TestUpdateUser_DisableAudited(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	actorID := 99

	if code := patchStatus(t, h, actorID, targetID, "disabled"); code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", code)
	}

	logs, err := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionUserDisable)).All(context.Background())
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 user.disable audit, got %d", len(logs))
	}
	lg := logs[0]
	if lg.ActorUserID != actorID {
		t.Errorf("actor = %d, want %d", lg.ActorUserID, actorID)
	}
	if lg.ResourceType != "user" || lg.ResourceID != targetID {
		t.Errorf("resource = %s/%d, want user/%d", lg.ResourceType, lg.ResourceID, targetID)
	}
	if lg.Detail["from"] != "active" || lg.Detail["to"] != "disabled" {
		t.Errorf("detail from/to = %v/%v, want active/disabled", lg.Detail["from"], lg.Detail["to"])
	}
}

// TestUpdateUser_EnableAudited 停用后再启用 → 记 user.enable。
func TestUpdateUser_EnableAudited(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	// 先停用
	if code := patchStatus(t, h, 1, targetID, "disabled"); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}
	// 再启用
	if code := patchStatus(t, h, 1, targetID, "active"); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
	enables, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionUserEnable)).All(context.Background())
	if len(enables) != 1 {
		t.Fatalf("expected 1 user.enable audit, got %d", len(enables))
	}
}

// TestUpdateUser_PhoneWritable B8：PATCH /users/:id 可写 User.phone（电话/短信通道解号依赖）。
func TestUpdateUser_PhoneWritable(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	e := echo.New()
	e.PATCH("/api/v1/users/:id", h.updateUser, RequireUser(true, nil))
	body := strings.NewReader(`{"phone":"+8613800138000"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+strconv.Itoa(targetID), body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH phone = %d, want 200", rec.Code)
	}
	u, err := c.User.Get(context.Background(), targetID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.Phone != "+8613800138000" {
		t.Errorf("phone = %q, want +8613800138000", u.Phone)
	}
}

// TestUpdateUser_NoStatusChangeNoAudit 提交相同 status → 不记审计（避免噪音）。
func TestUpdateUser_NoStatusChangeNoAudit(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	// 目标本就是 active，再提交 active，无变化
	if code := patchStatus(t, h, 1, targetID, "active"); code != http.StatusOK {
		t.Fatalf("patch = %d", code)
	}
	total, _ := c.AuditLog.Query().Count(context.Background())
	if total != 0 {
		t.Errorf("expected 0 audit logs for no-op status, got %d", total)
	}
}
