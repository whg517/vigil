// seed_admin.go 默认管理员种子（能力域 13），避免装完无法登录。
//
// 幂等策略：依赖 User.username 唯一约束，直接 Create 遇 ConstraintError 视为已存在跳过
// （与 SeedBuiltinRoles 一致，避免 Count→Create 两步竞态）。
//
// 安全（QA 审计 C8 / H1.6）：默认 admin 密码 changeme 仅应急，seed 时置
// must_change_password=true。登录后必须通过 POST /auth/change-password 改密，
// 否则中间件拦截业务 API（见 middleware.go RequireUser），杜绝 admin/changeme 长期可用。
package auth

import (
	"context"

	"github.com/kevin/vigil/ent"
)

// SeedDefaultAdmin 若无 admin 用户则创建默认管理员（username=admin, password=changeme）。
// 返回 (created, err)：created=true 表示本次新建了 admin（需提醒改密）。
// 幂等：已有 admin 则 created=false，无副作用。
// 新建的 admin 标记 must_change_password=true，强制首登改密。
func SeedDefaultAdmin(ctx context.Context, db *ent.Client) (created bool, err error) {
	_, err = db.User.Create().
		SetUsername("admin").
		SetName("Default Admin").
		SetEmail("admin@vigil.local").
		SetPasswordHash(HashPassword("changeme")).
		SetMustChangePassword(true).
		Save(ctx)
	if err != nil {
		// username 唯一约束冲突 = admin 已存在（并发或重复启动），幂等跳过
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
