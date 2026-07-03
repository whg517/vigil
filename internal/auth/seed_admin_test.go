package auth

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/user"

	_ "github.com/mattn/go-sqlite3"
)

// seedRoles 先 seed 内置角色（装配顺序，wire.go 同款：roles 先 admin 后）。
func seedRoles(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", dsn)
	t.Cleanup(func() { _ = c.Close() })
	if err := SeedBuiltinRoles(context.Background(), c); err != nil {
		t.Fatalf("SeedBuiltinRoles: %v", err)
	}
	return c
}

// TestSeedDefaultAdmin_CreatesAdmin 空库首次调用创建 admin，密码可校验。
func TestSeedDefaultAdmin_CreatesAdmin(t *testing.T) {
	c := seedRoles(t, "file:seed_admin_create2?mode=memory&cache=shared&_fk=1")
	ctx := context.Background()

	created, err := SeedDefaultAdmin(ctx, c)
	if err != nil {
		t.Fatalf("SeedDefaultAdmin: %v", err)
	}
	if !created {
		t.Error("first call created=false, want true")
	}
	admin, err := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	if err != nil {
		t.Fatalf("query admin: %v", err)
	}
	if !VerifyPassword("changeme", admin.PasswordHash) {
		t.Error("admin password is not changeme")
	}
	if !admin.MustChangePassword {
		t.Error("seeded admin must_change_password=false, want true")
	}
}

// TestSeedDefaultAdmin_BindsOrgAdminRole FIX-A：新建 admin 自动绑定 org_admin（org scope）。
// 修复前 admin 无角色，调业务 API 全部 403，阻断首次配置。
func TestSeedDefaultAdmin_BindsOrgAdminRole(t *testing.T) {
	c := seedRoles(t, "file:seed_admin_bind?mode=memory&cache=shared&_fk=1")
	ctx := context.Background()

	if _, err := SeedDefaultAdmin(ctx, c); err != nil {
		t.Fatalf("SeedDefaultAdmin: %v", err)
	}
	// admin 应有 org_admin 绑定（org scope）
	admin, _ := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	authz := NewAuthorizer(c)
	// org_admin 有全部权限，任意权限点都应通过
	ok, err := authz.Check(ctx, AuthzRequest{UserID: admin.ID, Permission: PermIncidentView})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("FIX-A: seeded admin should have incident.view via org_admin binding, got denied")
	}
	ok, _ = authz.Check(ctx, AuthzRequest{UserID: admin.ID, Permission: PermTeamCreate})
	if !ok {
		t.Error("FIX-A: seeded admin should have team.create via org_admin binding")
	}
}

// TestSeedDefaultAdmin_Idempotent 已有 admin 时再次调用幂等（created=false，无副作用）。
func TestSeedDefaultAdmin_Idempotent(t *testing.T) {
	c := seedRoles(t, "file:seed_admin_idem2?mode=memory&cache=shared&_fk=1")
	ctx := context.Background()

	if _, err := SeedDefaultAdmin(ctx, c); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// 用户改了密码
	admin, _ := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	_ = c.User.UpdateOneID(admin.ID).SetPasswordHash(HashPassword("new-pw")).Exec(ctx)
	// 首次绑定后的 RoleBinding 数
	bindingsBefore, _ := c.RoleBinding.Query().Count(ctx)

	created, err := SeedDefaultAdmin(ctx, c)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if created {
		t.Error("second call created=true, want false (idempotent)")
	}
	admin2, _ := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	if !VerifyPassword("new-pw", admin2.PasswordHash) {
		t.Error("second seed overwrote password, should be idempotent")
	}
	cnt, _ := c.User.Query().Count(ctx)
	if cnt != 1 {
		t.Errorf("user count=%d, want 1", cnt)
	}
	// 幂等：第二次不应新增绑定
	bindingsAfter, _ := c.RoleBinding.Query().Count(ctx)
	if bindingsAfter != bindingsBefore {
		t.Errorf("idempotent seed added bindings: before=%d after=%d", bindingsBefore, bindingsAfter)
	}
}
