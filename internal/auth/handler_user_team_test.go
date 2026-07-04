package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
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

// postJSON 走完整 echo 链路发一个 JSON POST，返回响应码 + body（测试辅助）。
func postUserJSON(t *testing.T, h *UserHandler, method, path, body string, actorID int, handler echo.HandlerFunc, routePattern string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	switch method {
	case http.MethodPost:
		e.POST(routePattern, handler, RequireUser(true, nil))
	case http.MethodPatch:
		e.PATCH(routePattern, handler, RequireUser(true, nil))
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(actorID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestCreateUser_Success 建用户成功 → 201 + must_change_password=true + 审计（M1，T2.6）。
func TestCreateUser_Success(t *testing.T) {
	c, h, _ := newUserHandlerTest(t)
	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"alice","email":"alice@x.com","name":"Alice","password":"Secret123"}`,
		1, h.createUser, "/api/v1/users")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	u, err := c.User.Query().Where(entuser.UsernameEQ("alice")).Only(context.Background())
	if err != nil {
		t.Fatalf("query created user: %v", err)
	}
	// 首登强制改密：管理员设的初始密码仅应急，用户首登须改。
	if !u.MustChangePassword {
		t.Error("created user must have must_change_password=true (首登强制改密)")
	}
	if u.Email != "alice@x.com" {
		t.Errorf("email = %q, want alice@x.com", u.Email)
	}
	// 建用户落审计（M1）。
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionUserCreate)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 user.create audit, got %d", len(logs))
	}
	if logs[0].ResourceName != "alice" || logs[0].ActorUserID != 1 {
		t.Errorf("audit = %s by %d, want alice by 1", logs[0].ResourceName, logs[0].ActorUserID)
	}
}

// TestCreateUser_DuplicateUsername409 重复 username → 409（不泄底层 SQL）。
func TestCreateUser_DuplicateUsername409(t *testing.T) {
	_, h, _ := newUserHandlerTest(t)
	// 第一次建成功
	if rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"bob","email":"bob@x.com","password":"Secret123"}`,
		1, h.createUser, "/api/v1/users"); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", rec.Code)
	}
	// 同 username 再建 → 409
	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"bob","email":"bob2@x.com","password":"Secret123"}`,
		1, h.createUser, "/api/v1/users")
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup username = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateUser_DuplicateEmail409 重复 email → 409。
func TestCreateUser_DuplicateEmail409(t *testing.T) {
	_, h, _ := newUserHandlerTest(t)
	if rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"c1","email":"same@x.com","password":"Secret123"}`,
		1, h.createUser, "/api/v1/users"); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d", rec.Code)
	}
	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"c2","email":"same@x.com","password":"Secret123"}`,
		1, h.createUser, "/api/v1/users")
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup email = %d, want 409", rec.Code)
	}
}

// TestCreateUser_WeakPassword400 初始密码过弱 → 400（管理员不能设首登也改不动的口令）。
func TestCreateUser_WeakPassword400(t *testing.T) {
	_, h, _ := newUserHandlerTest(t)
	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users",
		`{"username":"weak","email":"weak@x.com","password":"123"}`,
		1, h.createUser, "/api/v1/users")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password = %d, want 400", rec.Code)
	}
}

// TestResetPassword_RevokesOldToken 管理员重置密码 → token_version 自增使旧 token 失效
// + must_change_password=true + 审计（M1，T2.6，复用 T0.4 吊销机制）。
func TestResetPassword_RevokesOldToken(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	ctx := context.Background()
	// 记录重置前的 token_version（默认 0），据此签一枚"旧 token"。
	before, _ := c.User.Get(ctx, targetID)
	signer := NewJWTSigner("test-secret-please-change", time.Hour, 24*time.Hour)
	oldRefresh, err := signer.GenerateRefreshToken(targetID, before.TokenVersion)
	if err != nil {
		t.Fatalf("mint old refresh: %v", err)
	}

	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users/"+strconv.Itoa(targetID)+"/reset-password",
		`{"new_password":"NewSecret123"}`, 1, h.resetPassword, "/api/v1/users/:id/reset-password")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	after, _ := c.User.Get(ctx, targetID)
	// token_version 自增 → 旧 token（claims 停留旧版本）在 refresh 校验时版本不匹配被拒。
	if after.TokenVersion != before.TokenVersion+1 {
		t.Errorf("token_version = %d, want %d (自增吊销旧 token)", after.TokenVersion, before.TokenVersion+1)
	}
	// 模拟 refresh 校验：旧 token 的 token_version 与库中当前值不一致 = 已吊销。
	claims, err := signer.ParseToken(oldRefresh)
	if err != nil {
		t.Fatalf("parse old refresh: %v", err)
	}
	if claims.TokenVersion == after.TokenVersion {
		t.Error("old token still valid: token_version should differ after reset")
	}
	// 重置后须首登改密。
	if !after.MustChangePassword {
		t.Error("reset must set must_change_password=true")
	}
	// 落审计（M1）。
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionUserResetPassword)).All(ctx)
	if len(logs) != 1 {
		t.Fatalf("expected 1 user.reset_password audit, got %d", len(logs))
	}
}

// TestResetPassword_NotFound404 重置不存在用户 → 404。
func TestResetPassword_NotFound404(t *testing.T) {
	_, h, _ := newUserHandlerTest(t)
	rec := postUserJSON(t, h, http.MethodPost, "/api/v1/users/99999/reset-password",
		`{"new_password":"NewSecret123"}`, 1, h.resetPassword, "/api/v1/users/:id/reset-password")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reset missing user = %d, want 404", rec.Code)
	}
}

// === IM 解绑端点测试（M11）===

// stubUnbinder 记录解绑调用的假 IMAccountUnbinder（避免依赖 im 包，防 import cycle）。
type stubUnbinder struct {
	calls   []string // "userID:platform"
	removed bool     // Unbind 返回的 removed 值
	err     error
}

func (s *stubUnbinder) UnbindAccount(_ context.Context, userID int, platform string) (bool, error) {
	s.calls = append(s.calls, strconv.Itoa(userID)+":"+platform)
	return s.removed, s.err
}

// deleteIMAccount 走完整 echo 链路 DELETE /users/:id/im-accounts/:platform，返回响应码。
func deleteIMAccount(t *testing.T, h *UserHandler, actorID, targetID int, platform string) int {
	t.Helper()
	e := echo.New()
	e.DELETE("/api/v1/users/:id/im-accounts/:platform", h.unbindIMAccount, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/users/"+strconv.Itoa(targetID)+"/im-accounts/"+platform, nil)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(actorID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code
}

// TestUnbindIMAccount_SelfSuccess 本人解绑自己的 IM 账号 → 204 + 调用 unbinder + 审计。
func TestUnbindIMAccount_SelfSuccess(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	ub := &stubUnbinder{removed: true}
	h.SetIMAccountUnbinder(ub)

	// actor == target（本人自助），无需任何权限。
	if code := deleteIMAccount(t, h, targetID, targetID, "feishu"); code != http.StatusNoContent {
		t.Fatalf("self unbind = %d, want 204", code)
	}
	if len(ub.calls) != 1 || ub.calls[0] != strconv.Itoa(targetID)+":feishu" {
		t.Errorf("unbind calls = %v, want [%d:feishu]", ub.calls, targetID)
	}
	// 解绑落审计（M11）。
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionUserIMUnbind)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 user.im_unbind audit, got %d", len(logs))
	}
	if logs[0].Detail["platform"] != "feishu" || logs[0].ResourceID != targetID {
		t.Errorf("audit = platform=%v resource=%d, want feishu/%d", logs[0].Detail["platform"], logs[0].ResourceID, targetID)
	}
}

// TestUnbindIMAccount_NotBound404 该用户本无此平台绑定 → 404。
func TestUnbindIMAccount_NotBound404(t *testing.T) {
	_, h, targetID := newUserHandlerTest(t)
	ub := &stubUnbinder{removed: false} // 无可删对象
	h.SetIMAccountUnbinder(ub)
	if code := deleteIMAccount(t, h, targetID, targetID, "dingtalk"); code != http.StatusNotFound {
		t.Fatalf("unbind not-bound = %d, want 404", code)
	}
}

// TestUnbindIMAccount_OtherWithoutAuthzForbidden 无 authz 注入时解他人绑定 → 403（保守拒绝）。
func TestUnbindIMAccount_OtherWithoutAuthzForbidden(t *testing.T) {
	_, h, targetID := newUserHandlerTest(t) // 未注入 authz
	ub := &stubUnbinder{removed: true}
	h.SetIMAccountUnbinder(ub)
	// actor(999) != target → 无 authz 时不放行跨用户解绑。
	if code := deleteIMAccount(t, h, 999, targetID, "feishu"); code != http.StatusForbidden {
		t.Fatalf("other unbind without authz = %d, want 403", code)
	}
	if len(ub.calls) != 0 {
		t.Errorf("forbidden 时不应调用 unbinder，calls = %v", ub.calls)
	}
}

// TestUnbindIMAccount_OtherWithPermission admin（持 user.im.bind）解他人绑定 → 204。
func TestUnbindIMAccount_OtherWithPermission(t *testing.T) {
	c, h, targetID := newUserHandlerTest(t)
	ub := &stubUnbinder{removed: true}
	h.SetIMAccountUnbinder(ub)
	// 注入真实 authz + 给 actor 授 org 级 user.im.bind 角色。
	h.SetAuthorizer(NewAuthorizer(c))
	adminID := seedUserWithOrgPerm(t, c, PermUserIMBind)

	if code := deleteIMAccount(t, h, adminID, targetID, "feishu"); code != http.StatusNoContent {
		t.Fatalf("admin unbind other = %d, want 204", code)
	}
	if len(ub.calls) != 1 {
		t.Errorf("admin 解他人应调用 unbinder 1 次，got %v", ub.calls)
	}
}

// seedUserWithOrgPerm 建一个持有指定 org 级权限点的用户，返回其 ID（供越权/授权测试）。
func seedUserWithOrgPerm(t *testing.T, c *ent.Client, perm Permission) int {
	t.Helper()
	ctx := context.Background()
	u, err := c.User.Create().SetUsername("admin_" + string(perm)).SetEmail(string(perm) + "@x.com").
		SetStatus(entuser.StatusActive).Save(ctx)
	if err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	rl, err := c.Role.Create().SetName("r_" + string(perm)).
		SetScopeLevel(role.ScopeLevelOrg).SetPermissions([]string{string(perm)}).Save(ctx)
	if err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("seed role binding: %v", err)
	}
	return u.ID
}
