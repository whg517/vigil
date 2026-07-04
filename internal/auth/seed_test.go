package auth

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"

	_ "github.com/mattn/go-sqlite3"
)

func newSeedTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:seed_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestSeedBuiltinRoles 验证种子写入全部内置角色。
func TestSeedBuiltinRoles(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()

	if err := SeedBuiltinRoles(ctx, c); err != nil {
		t.Fatalf("SeedBuiltinRoles: %v", err)
	}

	wantRoles := []string{"org_admin", "team_admin", "responder", "responder_lead", "subscriber", "oncall"}
	for _, name := range wantRoles {
		rl, err := c.Role.Query().Where(role.NameEQ(name)).Only(ctx)
		if err != nil {
			t.Errorf("expected builtin role %q: %v", name, err)
			continue
		}
		if !rl.Builtin {
			t.Errorf("role %q should be builtin=true", name)
		}
	}
}

// TestSeedBuiltinRoles_Idempotent 验证重复种子是幂等的（不报错、不重复）。
func TestSeedBuiltinRoles_Idempotent(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()

	if err := SeedBuiltinRoles(ctx, c); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	count1, _ := c.Role.Query().Count(ctx)

	if err := SeedBuiltinRoles(ctx, c); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	count2, _ := c.Role.Query().Count(ctx)

	if count1 != count2 {
		t.Errorf("seed not idempotent: count1=%d count2=%d", count1, count2)
	}
}

// TestSeed_OrgAdminHasAllPermissions 验证 org_admin 拥有全部权限点。
func TestSeed_OrgAdminHasAllPermissions(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()
	_ = SeedBuiltinRoles(ctx, c)

	rl, err := c.Role.Query().Where(role.NameEQ("org_admin")).Only(ctx)
	if err != nil {
		t.Fatalf("get org_admin: %v", err)
	}
	if len(rl.Permissions) != len(AllPermissions) {
		t.Errorf("org_admin permissions: got %d, want %d (all)", len(rl.Permissions), len(AllPermissions))
	}
}

// TestSeed_ResponderLimited 验证 responder 只有有限权限（不含 admin 类）。
func TestSeed_ResponderLimited(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()
	_ = SeedBuiltinRoles(ctx, c)

	rl, _ := c.Role.Query().Where(role.NameEQ("responder")).Only(ctx)
	permSet := map[string]bool{}
	for _, p := range rl.Permissions {
		permSet[p] = true
	}

	// 应有 ack
	if !permSet["incident.ack"] {
		t.Error("responder should have incident.ack")
	}
	// 不应有 admin 级权限
	for _, admin := range []string{"admin.settings", "role.create", "team.delete", "service.delete"} {
		if permSet[admin] {
			t.Errorf("responder should NOT have %s", admin)
		}
	}
}

// TestSeed_SubscriberReadOnly 验证 subscriber 是只读（无任何写权限）。
func TestSeed_SubscriberReadOnly(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()
	_ = SeedBuiltinRoles(ctx, c)

	rl, _ := c.Role.Query().Where(role.NameEQ("subscriber")).Only(ctx)
	for _, p := range rl.Permissions {
		// 只读权限只应是 .view 类
		if perm := Permission(p); perm != PermIncidentView && perm != PermEventView && perm != PermPostmortemView {
			t.Errorf("subscriber should be read-only, got %s", p)
		}
	}
}

// TestPermIncidentClose_Valid 新增权限点 incident.close 应在 AllPermissions 内（IsValid）。
func TestPermIncidentClose_Valid(t *testing.T) {
	if !PermIncidentClose.IsValid() {
		t.Error("incident.close 应是合法权限点（未登记到 AllPermissions?）")
	}
}

// TestSeed_IncidentCloseGrants 验证 incident.close 授予处置负责人类角色，未泄漏给只读干系人。
// 补 closed 终态时新增的权限点：team_admin/responder_lead/org_admin 可关闭，subscriber 不可。
func TestSeed_IncidentCloseGrants(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()
	_ = SeedBuiltinRoles(ctx, c)

	has := func(roleName, perm string) bool {
		rl, err := c.Role.Query().Where(role.NameEQ(roleName)).Only(ctx)
		if err != nil {
			t.Fatalf("get role %q: %v", roleName, err)
		}
		for _, p := range rl.Permissions {
			if p == perm {
				return true
			}
		}
		return false
	}

	for _, r := range []string{"team_admin", "responder_lead", "org_admin"} {
		if !has(r, "incident.close") {
			t.Errorf("%s 应拥有 incident.close", r)
		}
	}
	if has("subscriber", "incident.close") {
		t.Error("subscriber 只读角色不应拥有 incident.close")
	}
}

// TestSeed_ScopeLevel 验证 org_admin 是 org 级，其他是 team 级。
func TestSeed_ScopeLevel(t *testing.T) {
	c := newSeedTestClient(t)
	ctx := context.Background()
	_ = SeedBuiltinRoles(ctx, c)

	orgAdmin, _ := c.Role.Query().Where(role.NameEQ("org_admin")).Only(ctx)
	if orgAdmin.ScopeLevel != role.ScopeLevelOrg {
		t.Error("org_admin should be org scope")
	}
	responder, _ := c.Role.Query().Where(role.NameEQ("responder")).Only(ctx)
	if responder.ScopeLevel != role.ScopeLevelTeam {
		t.Error("responder should be team scope")
	}
}
