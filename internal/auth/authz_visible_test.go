// authz_visible_test.go VisibleTeamIDs 测试（ARCH-02/SEC-01 list 数据隔离）。
package auth

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"

	_ "github.com/mattn/go-sqlite3"
)

// newVisibleClient 独立内存库（避免与 authz_test.go 的 rbac_test 库共享数据）。
func newVisibleClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:visible_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestVisibleTeamIDs_OrgWide 有 org 级 binding → orgWide=true（list 不过滤）。
func TestVisibleTeamIDs_OrgWide(t *testing.T) {
	c := newVisibleClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "admin", role.ScopeLevelOrg, []string{string(PermIncidentView)})
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelOrg, "", nil)

	authz := NewAuthorizer(c)
	teamIDs, orgWide, err := authz.VisibleTeamIDs(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("VisibleTeamIDs: %v", err)
	}
	if !orgWide {
		t.Error("user with org binding should be orgWide")
	}
	if len(teamIDs) != 0 {
		t.Errorf("orgWide should return no teamIDs (means all), got %v", teamIDs)
	}
}

// TestVisibleTeamIDs_TeamOnly 仅有 team 级 binding → 返回这些 team_id。
func TestVisibleTeamIDs_TeamOnly(t *testing.T) {
	c := newVisibleClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "responder", role.ScopeLevelTeam, []string{string(PermIncidentView)})
	// 绑定到 team 5 和 7（两个 team 级 binding）
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelTeam, "5", nil)
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelTeam, "7", nil)

	authz := NewAuthorizer(c)
	teamIDs, orgWide, err := authz.VisibleTeamIDs(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("VisibleTeamIDs: %v", err)
	}
	if orgWide {
		t.Error("team-only user should not be orgWide")
	}
	if len(teamIDs) != 2 {
		t.Fatalf("expected 2 teamIDs, got %d (%v)", len(teamIDs), teamIDs)
	}
	// 验证集合包含 5 和 7
	got := map[int]bool{}
	for _, id := range teamIDs {
		got[id] = true
	}
	if !got[5] || !got[7] {
		t.Errorf("expected teams {5,7}, got %v", teamIDs)
	}
}

// TestVisibleTeamIDs_NoBinding 无任何 binding → orgWide=false 且空列表。
func TestVisibleTeamIDs_NoBinding(t *testing.T) {
	c := newVisibleClient(t)
	u := seedUser(t, c)
	authz := NewAuthorizer(c)
	teamIDs, orgWide, err := authz.VisibleTeamIDs(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("VisibleTeamIDs: %v", err)
	}
	if orgWide {
		t.Error("no binding should not be orgWide")
	}
	if len(teamIDs) != 0 {
		t.Errorf("no binding should return empty, got %v", teamIDs)
	}
}

// TestVisibleTeamIDs_Dedup 重复绑定同一 team 应去重。
func TestVisibleTeamIDs_Dedup(t *testing.T) {
	c := newVisibleClient(t)
	u := seedUser(t, c)
	rl := createRoleWithPerms(t, c, "r", role.ScopeLevelTeam, []string{string(PermIncidentView)})
	// 同一 team 5 绑两次（不同角色场景）
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelTeam, "5", nil)
	bind(t, c, u.ID, rl.ID, rolebinding.ScopeLevelTeam, "5", nil)

	authz := NewAuthorizer(c)
	teamIDs, _, err := authz.VisibleTeamIDs(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("VisibleTeamIDs: %v", err)
	}
	if len(teamIDs) != 1 || teamIDs[0] != 5 {
		t.Errorf("expected dedup to [5], got %v", teamIDs)
	}
}
