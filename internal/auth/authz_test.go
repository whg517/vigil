package auth

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:rbac_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedUser 建一个用户。
func seedUser(t *testing.T, c *ent.Client) *ent.User {
	t.Helper()
	u, err := c.User.Create().SetUsername("alice").SetEmail("a@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// createRoleWithPerms 建一个含指定权限点的角色。
func createRoleWithPerms(t *testing.T, c *ent.Client, name string, scope role.ScopeLevel, perms []string) *ent.Role {
	t.Helper()
	rl, err := c.Role.Create().
		SetName(name).
		SetScopeLevel(scope).
		SetPermissions(perms).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	return rl
}

// bind 把角色授给用户（指定 scope）。
func bind(t *testing.T, c *ent.Client, userID, roleID int, scope rolebinding.ScopeLevel, teamID string, expiresAt *time.Time) {
	t.Helper()
	b := c.RoleBinding.Create().
		SetUserID(userID).
		SetRoleID(roleID).
		SetScopeLevel(scope).
		SetGrantedAt(time.Now())
	if teamID != "" {
		b.SetTeamID(teamID)
	}
	if expiresAt != nil {
		b.SetExpiresAt(*expiresAt)
	}
	if _, err := b.Save(context.Background()); err != nil {
		t.Fatalf("create binding: %v", err)
	}
}

// TestCheck_OrgScopeGranted 验证 org 级授权对所有团队生效。
func TestCheck_OrgScopeGranted(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "admin", role.ScopeLevelOrg, []string{string(PermIncidentAck), string(PermIncidentView)})
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelOrg, "", nil)

	authz := NewAuthorizer(c)

	// org 级权限，任意 team scope 都应通过
	teamID := 5
	ok, err := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck, TeamScope: &teamID})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("org-scope grant should be effective for any team")
	}

	// 无 team scope 也应通过
	ok, _ = authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentView})
	if !ok {
		t.Error("org-scope grant should be effective without team scope")
	}
}

// TestCheck_TeamScopeOnly 验证 team 级授权仅对匹配团队生效（软隔离核心）。
func TestCheck_TeamScopeOnly(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "responder", role.ScopeLevelTeam, []string{string(PermIncidentAck)})
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelTeam, "5", nil) // 团队 5

	authz := NewAuthorizer(c)

	team5 := 5
	team6 := 6
	// 同团队：通过
	ok, _ := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck, TeamScope: &team5})
	if !ok {
		t.Error("team-scope grant should be effective for matching team")
	}
	// 不同团队：拒绝（软隔离）
	ok, _ = authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck, TeamScope: &team6})
	if ok {
		t.Error("team-scope grant should NOT be effective for other team (soft isolation)")
	}
	// 无 team scope：拒绝
	ok, _ = authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck})
	if ok {
		t.Error("team-scope grant should not be effective without team scope")
	}
}

// TestCheck_NoPermission 验证无授权拒绝。
func TestCheck_NoPermission(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	authz := NewAuthorizer(c)

	ok, _ := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck})
	if ok {
		t.Error("unauthorized user should be denied")
	}
}

// TestCheck_ExpiredBinding 验证过期绑定不生效。
func TestCheck_ExpiredBinding(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "temp", role.ScopeLevelOrg, []string{string(PermIncidentAck)})
	past := time.Now().Add(-1 * time.Hour) // 1 小时前过期
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelOrg, "", &past)

	authz := NewAuthorizer(c)
	ok, _ := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck})
	if ok {
		t.Error("expired binding should not be effective")
	}
}

// TestCheck_PermissionNotInRole 验证有角色但无目标权限点时拒绝。
func TestCheck_PermissionNotInRole(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "viewer", role.ScopeLevelOrg, []string{string(PermIncidentView)}) // 只有 view
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelOrg, "", nil)

	authz := NewAuthorizer(c)
	// view 通过
	ok, _ := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentView})
	if !ok {
		t.Error("view should be granted")
	}
	// ack 拒绝（角色无此权限）
	ok, _ = authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: PermIncidentAck})
	if ok {
		t.Error("ack should be denied (not in role)")
	}
}

// TestCheck_UnionMerge 验证多角色权限并集（org + team 合并）。
func TestCheck_UnionMerge(t *testing.T) {
	c := newTestClient(t)
	u := seedUser(t, c)
	// org 级角色给 view
	rlOrg := createRoleWithPerms(t, c, "org-viewer", role.ScopeLevelOrg, []string{string(PermIncidentView)})
	bind(t, c, u.ID, rlOrg.ID, rolebinding.ScopeLevelOrg, "", nil)
	// team 级角色给 ack
	rlTeam := createRoleWithPerms(t, c, "team-ack", role.ScopeLevelTeam, []string{string(PermIncidentAck)})
	bind(t, c, u.ID, rlTeam.ID, rolebinding.ScopeLevelTeam, "3", nil)

	authz := NewAuthorizer(c)
	team3 := 3
	// 两个权限都应通过（并集）
	for _, p := range []Permission{PermIncidentView, PermIncidentAck} {
		ok, _ := authz.Check(context.Background(), AuthzRequest{UserID: u.ID, Permission: p, TeamScope: &team3})
		if !ok {
			t.Errorf("union: permission %s should be granted", p)
		}
	}
}

// TestPermission_IsValid 验证权限点合法性校验。
func TestPermission_IsValid(t *testing.T) {
	cases := map[Permission]bool{
		PermIncidentAck:      true,
		PermAdminSettings:    true,
		Permission("bogus"):  false,
		Permission(""):       false,
	}
	for p, want := range cases {
		if got := p.IsValid(); got != want {
			t.Errorf("IsValid(%q): got %v, want %v", p, got, want)
		}
	}
}
