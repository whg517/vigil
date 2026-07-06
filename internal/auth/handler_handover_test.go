// handler_handover_test.go 用户禁用交接预览契约测试（N2.3 / M13.1）。
//
// 覆盖 GET /users/:id/handover-preview：有待交接项 → 各类清单列出；无项 → 空且 has_items=false；
// 禁用（active→disabled）且有待交接项时 PATCH 响应回带 handover 提示；404。
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"
	entuser "github.com/kevin/vigil/ent/user"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// handoverResp 解析交接预览响应。
type handoverResp struct {
	UserID    int    `json:"user_id"`
	Username  string `json:"username"`
	Status    string `json:"status"`
	HasItems  bool   `json:"has_items"`
	Schedules []struct {
		ScheduleID   int    `json:"schedule_id"`
		ScheduleName string `json:"schedule_name"`
		RotationID   int    `json:"rotation_id"`
	} `json:"schedules"`
	ActionItems []struct {
		ActionItemID int    `json:"action_item_id"`
		Status       string `json:"status"`
	} `json:"action_items"`
	RoleBindings []struct {
		BindingID int    `json:"binding_id"`
		RoleName  string `json:"role_name"`
		Temporary bool   `json:"temporary"`
	} `json:"role_bindings"`
	IMBindings []struct {
		Platform  string `json:"platform"`
		AccountID string `json:"account_id"`
	} `json:"im_bindings"`
}

// getHandover 走完整 echo 链路 GET /users/:id/handover-preview。
func getHandover(t *testing.T, h *UserHandler, targetID int) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.GET("/api/v1/users/:id/handover-preview", h.handoverPreview, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+strconv.Itoa(targetID)+"/handover-preview", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func decodeHandover(t *testing.T, rec *httptest.ResponseRecorder) handoverResp {
	t.Helper()
	var r handoverResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode handover: %v (body=%s)", err, rec.Body.String())
	}
	return r
}

// TestHandoverPreview_WithItems 有全部四类待交接项 → 各类清单列出 + has_items=true。
func TestHandoverPreview_WithItems(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_with?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	u := c.User.Create().SetUsername("leaving").SetEmail("l@x.com").
		SetStatus(entuser.StatusActive).SaveX(ctx)

	// ① 排班：建 Schedule + Rotation，把 u 加入 participants。
	sch := c.Schedule.Create().SetName("payments-oncall").SetType("rotation").SaveX(ctx)
	rot := c.Rotation.Create().SetName("primary").SetRotationType("daily").
		SetStartDate(time.Now()).AddParticipantIDs(u.ID).SaveX(ctx)
	c.Schedule.UpdateOneID(sch.ID).AddRotationIDs(rot.ID).ExecX(ctx)

	// ② ActionItem：u 为 owner 的未完成项 + 一个已完成项（不应列出）。
	pm := c.Postmortem.Create().SetSections(map[string]any{}).SaveX(ctx)
	openAI := c.ActionItem.Create().SetDescription("fix runbook").
		SetOwnerID(strconv.Itoa(u.ID)).SetStatus("in_progress").SaveX(ctx)
	c.Postmortem.UpdateOneID(pm.ID).AddActionItemIDs(openAI.ID).ExecX(ctx)
	doneAI := c.ActionItem.Create().SetDescription("done thing").
		SetOwnerID(strconv.Itoa(u.ID)).SetStatus("done").SaveX(ctx)
	c.Postmortem.UpdateOneID(pm.ID).AddActionItemIDs(doneAI.ID).ExecX(ctx)

	// ③ RoleBinding：一条常规 org 绑定 + 一条未过期临时绑定 + 一条已过期（不应列出）。
	rl := c.Role.Create().SetName("responder").SetScopeLevel("org").
		SetPermissions([]string{string(PermIncidentAck)}).SaveX(ctx)
	c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
		SetScopeLevel("org").SetGrantedAt(time.Now()).SaveX(ctx)
	c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
		SetScopeLevel("org").SetGrantedAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx) // 临时未过期
	c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
		SetScopeLevel("org").SetGrantedAt(time.Now().Add(-2 * time.Hour)).
		SetExpiresAt(time.Now().Add(-time.Hour)).SaveX(ctx) // 已过期，过滤掉

	// ④ IM 绑定。
	c.IMAccountBinding.Create().SetUserID(u.ID).SetPlatform("feishu").
		SetAccountID("ou_123").SaveX(ctx)

	h := NewUserHandler(c)
	rec := getHandover(t, h, u.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("handover preview: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	r := decodeHandover(t, rec)
	if !r.HasItems {
		t.Error("has_items should be true")
	}
	if len(r.Schedules) != 1 || r.Schedules[0].ScheduleID != sch.ID || r.Schedules[0].RotationID != rot.ID {
		t.Errorf("schedules: got %+v, want 1 item {sched:%d,rot:%d}", r.Schedules, sch.ID, rot.ID)
	}
	if len(r.ActionItems) != 1 || r.ActionItems[0].ActionItemID != openAI.ID {
		t.Errorf("action_items: got %+v, want only the unfinished one (%d)", r.ActionItems, openAI.ID)
	}
	// 常规 + 临时未过期 = 2；已过期被过滤。
	if len(r.RoleBindings) != 2 {
		t.Errorf("role_bindings: got %d, want 2 (regular + non-expired temp, expired filtered)", len(r.RoleBindings))
	}
	tempCount := 0
	for _, rb := range r.RoleBindings {
		if rb.Temporary {
			tempCount++
		}
		if rb.RoleName != "responder" {
			t.Errorf("role_name = %q, want responder", rb.RoleName)
		}
	}
	if tempCount != 1 {
		t.Errorf("expected exactly 1 temporary binding, got %d", tempCount)
	}
	if len(r.IMBindings) != 1 || r.IMBindings[0].Platform != "feishu" || r.IMBindings[0].AccountID != "ou_123" {
		t.Errorf("im_bindings: got %+v, want feishu/ou_123", r.IMBindings)
	}
}

// TestHandoverPreview_Empty 无任何待交接项 → 各清单空 + has_items=false。
func TestHandoverPreview_Empty(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_empty?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u := c.User.Create().SetUsername("clean").SetEmail("c@x.com").
		SetStatus(entuser.StatusActive).SaveX(ctx)

	h := NewUserHandler(c)
	rec := getHandover(t, h, u.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("handover preview: got %d, want 200", rec.Code)
	}
	r := decodeHandover(t, rec)
	if r.HasItems {
		t.Error("has_items should be false for user with no归属")
	}
	if len(r.Schedules) != 0 || len(r.ActionItems) != 0 || len(r.RoleBindings) != 0 || len(r.IMBindings) != 0 {
		t.Errorf("all lists should be empty, got %+v", r)
	}
	if r.Status != "active" {
		t.Errorf("status = %q, want active", r.Status)
	}
}

// TestHandoverPreview_NotFound 不存在用户 → 404。
func TestHandoverPreview_NotFound(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_404?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewUserHandler(c)
	rec := getHandover(t, h, 99999)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("handover missing user: got %d, want 404", rec.Code)
	}
}

// TestHandoverPreview_ActionItemOwnerScoping 只列该用户 owner 的项，不串到别的 owner。
func TestHandoverPreview_ActionItemOwnerScoping(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_owner?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u := c.User.Create().SetUsername("u1").SetEmail("u1@x.com").SaveX(ctx)
	other := c.User.Create().SetUsername("u2").SetEmail("u2@x.com").SaveX(ctx)
	pm := c.Postmortem.Create().SetSections(map[string]any{}).SaveX(ctx)
	mine := c.ActionItem.Create().SetDescription("mine").
		SetOwnerID(strconv.Itoa(u.ID)).SetStatus("open").SaveX(ctx)
	theirs := c.ActionItem.Create().SetDescription("theirs").
		SetOwnerID(strconv.Itoa(other.ID)).SetStatus("open").SaveX(ctx)
	c.Postmortem.UpdateOneID(pm.ID).AddActionItemIDs(mine.ID, theirs.ID).ExecX(ctx)

	h := NewUserHandler(c)
	r := decodeHandover(t, getHandover(t, h, u.ID))
	if len(r.ActionItems) != 1 || r.ActionItems[0].ActionItemID != mine.ID {
		t.Errorf("action_items: got %+v, want only mine (%d)", r.ActionItems, mine.ID)
	}
}

// TestUpdateUser_DisableWarnsPendingHandover 禁用有待交接项的用户 → 200 且响应体带 handover 提示。
func TestUpdateUser_DisableWarnsPendingHandover(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_disable_warn?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u := c.User.Create().SetUsername("bye").SetEmail("bye@x.com").
		SetStatus(entuser.StatusActive).SaveX(ctx)
	// 造一个 IM 绑定作为待交接项。
	c.IMAccountBinding.Create().SetUserID(u.ID).SetPlatform("dingtalk").
		SetAccountID("dd_1").SaveX(ctx)

	h := NewUserHandler(c)
	e := echo.New()
	e.PATCH("/api/v1/users/:id", h.updateUser, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+strconv.Itoa(u.ID),
		strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Status   string        `json:"status"`
		Handover *handoverResp `json:"handover"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if body.Status != "disabled" {
		t.Errorf("status = %q, want disabled", body.Status)
	}
	if body.Handover == nil || !body.Handover.HasItems {
		t.Fatalf("disable with pending items should return handover warning, got %+v", body.Handover)
	}
	if len(body.Handover.IMBindings) != 1 {
		t.Errorf("handover im_bindings: got %d, want 1", len(body.Handover.IMBindings))
	}
}

// TestUpdateUser_DisableNoItemsNoWarning 禁用无待交接项的用户 → 200 且响应体无 handover 字段（裸 User）。
func TestUpdateUser_DisableNoItemsNoWarning(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:handover_disable_clean?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u := c.User.Create().SetUsername("clean2").SetEmail("clean2@x.com").
		SetStatus(entuser.StatusActive).SaveX(ctx)

	h := NewUserHandler(c)
	e := echo.New()
	e.PATCH("/api/v1/users/:id", h.updateUser, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+strconv.Itoa(u.ID),
		strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"handover"`) {
		t.Errorf("no pending items → response must not contain handover field, got %s", rec.Body.String())
	}
}
