package auth

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/user"

	_ "github.com/mattn/go-sqlite3"
)

// TestSeedDefaultAdmin_CreatesAdmin 空库首次调用创建 admin，密码可校验。
func TestSeedDefaultAdmin_CreatesAdmin(t *testing.T) {
	// 每个测试用独立内存库（不同 DSN 避免共享状态）
	c := enttest.Open(t, "sqlite3", "file:seed_admin_create?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	created, err := SeedDefaultAdmin(ctx, c)
	if err != nil {
		t.Fatalf("SeedDefaultAdmin: %v", err)
	}
	if !created {
		t.Error("first call created=false, want true")
	}
	// admin 存在且密码是 changeme
	admin, err := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	if err != nil {
		t.Fatalf("query admin: %v", err)
	}
	if !VerifyPassword("changeme", admin.PasswordHash) {
		t.Error("admin password is not changeme")
	}
	// QA 审计 C8：seed 的 admin 必须置 must_change_password=true（强制首登改密）
	if !admin.MustChangePassword {
		t.Error("seeded admin must_change_password=false, want true")
	}
}

// TestSeedDefaultAdmin_Idempotent 已有 admin 时再次调用幂等（created=false，无副作用）。
func TestSeedDefaultAdmin_Idempotent(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:seed_admin_idem?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	if _, err := SeedDefaultAdmin(ctx, c); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// 用户改了密码
	admin, _ := c.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	_ = c.User.UpdateOneID(admin.ID).SetPasswordHash(HashPassword("new-pw")).Exec(ctx)

	// 再次 seed：应跳过，不改密码
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
	// 全库仍只有 1 个用户
	cnt, _ := c.User.Query().Count(ctx)
	if cnt != 1 {
		t.Errorf("user count=%d, want 1", cnt)
	}
}
