// handler_role_team_test.go 角色编辑（T2.7）+ 团队成员管理（T2.7/S15）测试。
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
	entteam "github.com/kevin/vigil/ent/team"
	entuser "github.com/kevin/vigil/ent/user"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// === 角色编辑（PATCH /roles/:id，T2.7/M2）===

// newRoleHandlerTest 起一个装好审计器的 RBAC Handler。
func newRoleHandlerTest(t *testing.T) (*ent.Client, *Handler) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:role_edit_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewHandler(c)
	h.SetAuditRecorder(NewAuditRecorder(c))
	return c, h
}

// patchRole 走 echo 链路 PATCH /roles/:id。
func patchRole(t *testing.T, h *Handler, roleID int, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.PATCH("/api/v1/roles/:id", h.updateRole, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/roles/"+strconv.Itoa(roleID), strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestUpdateRole_Success 编辑自定义角色的权限集 + 名称 → 200 + 库更新 + 审计。
func TestUpdateRole_Success(t *testing.T) {
	c, h := newRoleHandlerTest(t)
	rl, err := c.Role.Create().
		SetName("payer_l1").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(PermIncidentView)}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed role: %v", err)
	}
	rec := patchRole(t, h, rl.ID,
		`{"name":"payer_l1_v2","permissions":["incident.view","incident.ack","runbook.execute"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch role = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := c.Role.Get(context.Background(), rl.ID)
	if got.Name != "payer_l1_v2" {
		t.Errorf("name = %q, want payer_l1_v2", got.Name)
	}
	if len(got.Permissions) != 3 {
		t.Errorf("permissions = %v, want 3 items (全量替换)", got.Permissions)
	}
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionRoleUpdate)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 role.update audit, got %d", len(logs))
	}
}

// TestUpdateRole_BuiltinForbidden 内置角色不可编辑 → 403（可复制不可改）。
func TestUpdateRole_BuiltinForbidden(t *testing.T) {
	c, h := newRoleHandlerTest(t)
	if err := SeedBuiltinRoles(context.Background(), c); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}
	admin, _ := c.Role.Query().Where(role.NameEQ("org_admin")).Only(context.Background())
	rec := patchRole(t, h, admin.ID, `{"name":"hacked"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("patch builtin role = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	// 未被改动。
	got, _ := c.Role.Get(context.Background(), admin.ID)
	if got.Name != "org_admin" {
		t.Errorf("builtin role name changed to %q, must stay org_admin", got.Name)
	}
}

// TestUpdateRole_InvalidPermission400 权限集含非法权限点 → 400。
func TestUpdateRole_InvalidPermission400(t *testing.T) {
	c, h := newRoleHandlerTest(t)
	rl, _ := c.Role.Create().
		SetName("custom").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(PermIncidentView)}).
		Save(context.Background())
	rec := patchRole(t, h, rl.ID, `{"permissions":["incident.view","not.a.real.perm"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch invalid perm = %d, want 400", rec.Code)
	}
}

// TestUpdateRole_NotFound404 编辑不存在角色 → 404。
func TestUpdateRole_NotFound404(t *testing.T) {
	_, h := newRoleHandlerTest(t)
	rec := patchRole(t, h, 99999, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing role = %d, want 404", rec.Code)
	}
}

// === 团队成员管理（POST/DELETE /teams/:id/members，T2.7/M3/S15）===

// seedTeamMemberEnv 建两个团队 + 一个管理员 actor + 一个待加成员 + 一个角色（仅 team.member.manage），
// 并把 actor 绑定为 teamA 的成员管理员（team scope）。
// 返回 client、TeamHandler（注入 authz）、teamA/teamB id、待加成员 uid、actor uid。
func seedTeamMemberEnv(t *testing.T) (c *ent.Client, h *TeamHandler, teamAID, teamBID, memberID, actorID int) {
	t.Helper()
	c = enttest.Open(t, "sqlite3", "file:team_member_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	teamA, _ := c.Team.Create().SetName("A").SetSlug("team-a").Save(ctx)
	teamB, _ := c.Team.Create().SetName("B").SetSlug("team-b").Save(ctx)
	// actor：team A 的成员管理员（仅对 teamA 有 team.member.manage）。
	actor, _ := c.User.Create().SetUsername("mgr").SetEmail("mgr@x.com").
		SetStatus(entuser.StatusActive).Save(ctx)
	// 待加入的普通成员。
	member, _ := c.User.Create().SetUsername("newbie").SetEmail("newbie@x.com").
		SetStatus(entuser.StatusActive).Save(ctx)
	rl, _ := c.Role.Create().SetName("team_mgr").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(PermTeamMemberManage)}).Save(ctx)
	// 绑定 actor 到 teamA（team scope）。
	_, _ = c.RoleBinding.Create().SetUserID(actor.ID).SetRoleID(rl.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(teamA.ID)).
		SetGrantedAt(time.Now()).Save(ctx)

	h = NewTeamHandler(c)
	h.SetAuditRecorder(NewAuditRecorder(c))
	h.SetAuthorizer(NewAuthorizer(c))
	return c, h, teamA.ID, teamB.ID, member.ID, actor.ID
}

// callMember 走 echo 链路调用成员端点（含 RouteGuard 权限门禁模拟由 authz 在 handler 内做 scope）。
// method=POST → addMember；method=DELETE → removeMember。actorID 注入为 X-Vigil-User-ID。
func callMember(t *testing.T, h *TeamHandler, method string, teamID, memberID, actorID int) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.POST("/api/v1/teams/:id/members", h.addMember, RequireUser(true, nil))
	e.DELETE("/api/v1/teams/:id/members/:uid", h.removeMember, RequireUser(true, nil))
	var req *http.Request
	if method == http.MethodPost {
		body := `{"user_id":` + strconv.Itoa(memberID) + `}`
		req = httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+strconv.Itoa(teamID)+"/members", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	} else {
		req = httptest.NewRequest(http.MethodDelete,
			"/api/v1/teams/"+strconv.Itoa(teamID)+"/members/"+strconv.Itoa(memberID), nil)
	}
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(actorID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestAddMember_Success team_admin 对自己团队加成员 → 204 + 建立成员关系 + 审计。
func TestAddMember_Success(t *testing.T) {
	c, h, teamA, _, memberID, actorID := seedTeamMemberEnv(t)
	rec := callMember(t, h, http.MethodPost, teamA, memberID, actorID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add member = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// 成员关系建立。
	n, _ := c.User.Query().Where(entuser.HasTeamsWith(entteam.IDEQ(teamA)), entuser.IDEQ(memberID)).Count(context.Background())
	if n != 1 {
		t.Errorf("member not attached to team A (count=%d)", n)
	}
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionTeamMemberAdd)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 team.member.add audit, got %d", len(logs))
	}
}

// TestAddMember_CrossTeamForbidden teamA 的 team_admin 对 teamB 加成员 → 403（团队软隔离）。
func TestAddMember_CrossTeamForbidden(t *testing.T) {
	c, h, _, teamB, memberID, actorID := seedTeamMemberEnv(t)
	rec := callMember(t, h, http.MethodPost, teamB, memberID, actorID)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-team add = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	// 未加入 teamB。
	n, _ := c.User.Query().Where(entuser.HasTeamsWith(entteam.IDEQ(teamB))).Count(context.Background())
	if n != 0 {
		t.Errorf("cross-team add should not attach member (count=%d)", n)
	}
}

// TestRemoveMember_Success team_admin 移除自己团队成员 → 204 + 关系解除 + 审计。
func TestRemoveMember_Success(t *testing.T) {
	c, h, teamA, _, memberID, actorID := seedTeamMemberEnv(t)
	// 先加入。
	if rec := callMember(t, h, http.MethodPost, teamA, memberID, actorID); rec.Code != http.StatusNoContent {
		t.Fatalf("precondition add = %d", rec.Code)
	}
	rec := callMember(t, h, http.MethodDelete, teamA, memberID, actorID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove member = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	n, _ := c.User.Query().Where(entuser.HasTeamsWith(entteam.IDEQ(teamA)), entuser.IDEQ(memberID)).Count(context.Background())
	if n != 0 {
		t.Errorf("member still attached after remove (count=%d)", n)
	}
	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(ActionTeamMemberRemove)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 team.member.remove audit, got %d", len(logs))
	}
}

// TestRemoveMember_CrossTeamForbidden teamA 管理员移除 teamB 成员 → 403。
func TestRemoveMember_CrossTeamForbidden(t *testing.T) {
	_, h, _, teamB, memberID, actorID := seedTeamMemberEnv(t)
	rec := callMember(t, h, http.MethodDelete, teamB, memberID, actorID)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-team remove = %d, want 403", rec.Code)
	}
}

// TestAddMember_TeamNotFound404 加成员到不存在团队 → 404。
func TestAddMember_TeamNotFound404(t *testing.T) {
	_, h, _, _, memberID, actorID := seedTeamMemberEnv(t)
	rec := callMember(t, h, http.MethodPost, 99999, memberID, actorID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("add to missing team = %d, want 404", rec.Code)
	}
}
