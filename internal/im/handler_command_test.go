package im

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
)

// --- 测试桩：RunbookTrigger / OncallResolver ---

// stubRunbookTrigger 记录 Execute 调用参数，返回可配的结果。
// 用于断言：IM 触发 runbook 时 approved 恒 false（写操作不在 IM 内放行）。
type stubRunbookTrigger struct {
	lookupErr    error
	lookupTeamID int
	result       *RunbookRunResult
	execErr      error
	// 记录入参供断言
	gotApproved bool
	gotRunbook  int
	gotIncident int
	execCalled  bool
}

func (s *stubRunbookTrigger) LookupByName(_ context.Context, _ string) (int, int, error) {
	if s.lookupErr != nil {
		return 0, 0, s.lookupErr
	}
	return 42, s.lookupTeamID, nil
}

func (s *stubRunbookTrigger) Execute(_ context.Context, runbookID, incID int, approved bool, _ int) (*RunbookRunResult, error) {
	s.execCalled = true
	s.gotApproved = approved
	s.gotRunbook = runbookID
	s.gotIncident = incID
	if s.execErr != nil {
		return nil, s.execErr
	}
	return s.result, nil
}

// stubOncallResolver 返回可配的值班行 / 错误。
type stubOncallResolver struct {
	teamLines    []string
	teamErr      error
	teamNameLine []string
	teamNameErr  error
	svcNameLine  []string
	svcNameErr   error
}

func (s *stubOncallResolver) OncallForTeam(_ context.Context, _ int) ([]string, error) {
	return s.teamLines, s.teamErr
}
func (s *stubOncallResolver) OncallForTeamName(_ context.Context, _ string) ([]string, error) {
	return s.teamNameLine, s.teamNameErr
}
func (s *stubOncallResolver) OncallForServiceName(_ context.Context, _ string) ([]string, error) {
	return s.svcNameLine, s.svcNameErr
}

// seedRunbookPerm 建团队 + 用户(绑 feishu) + incident + 授某权限点的 team 级角色（grant=""则不授）。
func seedRunbookPerm(t *testing.T, c *ent.Client, grant string) (incID, userID, teamID int) {
	t.Helper()
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("支付").SetSlug("payrb").Save(ctx)
	teamID = team.ID
	u, err := c.User.Create().
		SetUsername("zs").SetEmail("zs@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "feishu", AccountID: "ou_zs"}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID = u.ID
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").SetTitle("db down").
		SetSeverity(incident.SeverityCritical).SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	incID = inc.ID
	if grant != "" {
		rl, err := c.Role.Create().SetName("cmdrole").SetScopeLevel(role.ScopeLevelTeam).
			SetPermissions([]string{grant}).Save(ctx)
		if err != nil {
			t.Fatalf("create role: %v", err)
		}
		if _, err := c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(team.ID)).
			SetGrantedAt(time.Now()).Save(ctx); err != nil {
			t.Fatalf("bind role: %v", err)
		}
	}
	return
}

// postCommand 发一条斜杠命令回调（stubBot.ParseCallback 直接反序列化 IMEvent）。
func postCommand(t *testing.T, h *Handler, evt IMEvent) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	payload := mustJSON(t, evt)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/feishu/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)
	return rec
}

// TestRunbookCommand_ReadonlyExecuted 只读诊断 runbook：授 runbook.execute → 执行成功（approved=false）。
func TestRunbookCommand_ReadonlyExecuted(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedRunbookPerm(t, c, string(auth.PermRunbookExecute))
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	rb := &stubRunbookTrigger{result: &RunbookRunResult{PendingApproval: false, StepSummaries: []string{"✅ 诊断"}}}
	h.SetRunbookTrigger(rb)

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "runbook", CommandArg: "diag-rb " + strconv.Itoa(incID),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !rb.execCalled {
		t.Fatal("runbook Execute should have been called")
	}
	// ★ 核心安全断言：IM 触发 runbook 时 approved 恒 false（写操作不在 IM 内放行）。
	if rb.gotApproved {
		t.Error("IM runbook trigger must pass approved=false (never bypass approval)")
	}
	if rb.gotIncident != incID {
		t.Errorf("incident = %d, want %d", rb.gotIncident, incID)
	}
	// 应回一张结果卡片。
	if bot.sendCount == 0 {
		t.Error("expected a result card to be sent")
	}
}

// TestRunbookCommand_WriteRequiresApprovalNotBypassed 写操作 runbook：引擎返回 PendingApproval，
// IM 不放行——卡片提示须回 Web 审批，且 approved 传入的仍是 false。
func TestRunbookCommand_WriteRequiresApprovalNotBypassed(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedRunbookPerm(t, c, string(auth.PermRunbookExecute))
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	// 引擎报告有写步骤被审批闸门阻断。
	rb := &stubRunbookTrigger{result: &RunbookRunResult{PendingApproval: true, StepSummaries: []string{"⏸ 待审批 回滚"}}}
	h.SetRunbookTrigger(rb)

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "runbook", CommandArg: "rollback-rb " + strconv.Itoa(incID),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// ★ 绝不在 IM 内绕过审批：approved 必须是 false。
	if rb.gotApproved {
		t.Fatal("write runbook via IM must NOT bypass approval (approved must be false)")
	}
	// 结果卡片应带待审批提示。
	if bot.sendCount == 0 {
		t.Fatal("expected a result card to be sent")
	}
	last := bot.sentCards[len(bot.sentCards)-1]
	if last.StatusBadge == "" || !containsSub(last.StatusBadge, "审批") {
		t.Errorf("result card should indicate approval needed, got badge %q", last.StatusBadge)
	}
}

// TestRunbookCommand_Forbidden 无 runbook.execute 权限 → 403，且引擎不被调用。
func TestRunbookCommand_Forbidden(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedRunbookPerm(t, c, "") // 不授权
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	rb := &stubRunbookTrigger{result: &RunbookRunResult{}}
	h.SetRunbookTrigger(rb)

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "runbook", CommandArg: "diag-rb " + strconv.Itoa(incID),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if rb.execCalled {
		t.Error("runbook Execute must not be called when forbidden")
	}
}

// TestRunbookCommand_InvalidArgs 缺参数 → 400 用法提示。
func TestRunbookCommand_InvalidArgs(t *testing.T) {
	c := newHandlerClient(t)
	seedRunbookPerm(t, c, string(auth.PermRunbookExecute))
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	h.SetRunbookTrigger(&stubRunbookTrigger{result: &RunbookRunResult{}})

	// 只有 runbook 名，缺 incident。
	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "runbook", CommandArg: "diag-rb",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRunbookCommand_RunbookNotFound runbook 名不存在 → 404。
func TestRunbookCommand_RunbookNotFound(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedRunbookPerm(t, c, string(auth.PermRunbookExecute))
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	rb := &stubRunbookTrigger{lookupErr: errors.New("not found")}
	h.SetRunbookTrigger(rb)

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "runbook", CommandArg: "ghost-rb " + strconv.Itoa(incID),
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if rb.execCalled {
		t.Error("Execute must not run when runbook lookup failed")
	}
}

// TestOncallCommand_NoArgReturnsOncall 无参 oncall：授 schedule.view → 查调用者团队值班返回。
func TestOncallCommand_NoArgReturnsOncall(t *testing.T) {
	c := newHandlerClient(t)
	seedUserTeamMembership(t, c)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	h.SetOncallResolver(&stubOncallResolver{teamLines: []string{"一线：张三"}})

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "oncall", CommandArg: "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if bot.sendCount == 0 {
		t.Fatal("expected an oncall result card")
	}
	last := bot.sentCards[len(bot.sentCards)-1]
	found := false
	for _, r := range last.Rows {
		if r.Value == "一线：张三" {
			found = true
		}
	}
	if !found {
		t.Errorf("oncall card should contain the oncall line, rows=%+v", last.Rows)
	}
}

// TestOncallCommand_ByTeamName 有参 oncall：按团队名查值班。
func TestOncallCommand_ByTeamName(t *testing.T) {
	c := newHandlerClient(t)
	seedUserTeamMembership(t, c)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	h.SetOncallResolver(&stubOncallResolver{teamNameLine: []string{"值班群：李四"}})

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "oncall", CommandArg: "支付",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	last := bot.sentCards[len(bot.sentCards)-1]
	if len(last.Rows) == 0 || last.Rows[0].Value != "值班群：李四" {
		t.Errorf("expected oncall line for team, rows=%+v", last.Rows)
	}
}

// TestOncallCommand_Forbidden 团队成员但无 schedule.view 权限 → 403（无参 oncall 走团队汇总）。
func TestOncallCommand_Forbidden(t *testing.T) {
	c := newHandlerClient(t)
	// 建团队成员但不授 schedule.view。
	seedUserTeamMembershipGrant(t, c, false)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	h.SetOncallResolver(&stubOncallResolver{teamLines: []string{"x"}})

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "oncall", CommandArg: "",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestOncallCommand_NotFound 团队/服务名都查不到 → 404。
func TestOncallCommand_NotFound(t *testing.T) {
	c := newHandlerClient(t)
	seedUserTeamMembership(t, c)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	// 团队名、服务名解析均失败。
	h.SetOncallResolver(&stubOncallResolver{
		teamNameErr: errors.New("no team"),
		svcNameErr:  errors.New("no service"),
	})

	rec := postCommand(t, h, IMEvent{
		Type: EventCommand, Platform: "feishu", UnionID: "ou_zs", ChannelID: "chat_id:oc_c",
		Command: "oncall", CommandArg: "ghost",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// seedUserTeamMembership 建用户(绑 feishu) + 团队成员关系 + 授 schedule.view（team 级）。
func seedUserTeamMembership(t *testing.T, c *ent.Client) (userID, teamID int) {
	return seedUserTeamMembershipGrant(t, c, true)
}

// seedUserTeamMembershipGrant 同上，grant=false 时只建团队成员关系但不授 schedule.view
// （用于无权测试：用户在团队里但缺权限，应 403 而非 400）。
func seedUserTeamMembershipGrant(t *testing.T, c *ent.Client, grant bool) (userID, teamID int) {
	t.Helper()
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("支付").SetSlug("payoncall").Save(ctx)
	teamID = team.ID
	u, err := c.User.Create().
		SetUsername("zs").SetEmail("zs@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "feishu", AccountID: "ou_zs"}}).
		AddTeamIDs(team.ID). // 团队成员（无参 oncall 靠此查所属团队）
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID = u.ID
	if grant {
		// 授 schedule.view（team 级）。
		rl, _ := c.Role.Create().SetName("viewer").SetScopeLevel(role.ScopeLevelTeam).
			SetPermissions([]string{string(auth.PermScheduleView)}).Save(ctx)
		if _, err := c.RoleBinding.Create().SetUserID(u.ID).SetRoleID(rl.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(team.ID)).
			SetGrantedAt(time.Now()).Save(ctx); err != nil {
			t.Fatalf("bind role: %v", err)
		}
	}
	return
}

// containsSub 简单子串判断（避免引 strings 只为一处）。
func containsSub(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
