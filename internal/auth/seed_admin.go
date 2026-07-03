// seed_admin.go 默认管理员种子（能力域 13），避免装完无法登录。
//
// 幂等策略：依赖 User.username 唯一约束，直接 Create 遇 ConstraintError 视为已存在跳过
// （与 SeedBuiltinRoles 一致，避免 Count→Create 两步竞态）。
//
// 安全（QA 审计 C8 / H1.6）：默认 admin 密码 changeme 仅应急，seed 时置
// must_change_password=true。登录后必须通过 POST /auth/change-password 改密，
// 否则中间件拦截业务 API（见 middleware.go RequireUser），杜绝 admin/changeme 长期可用。
//
// FIX-A：新建 admin 时自动绑定 org_admin 角色（org scope）。
// 修复前 admin 无任何角色绑定，改密后调业务 API 全部 403 forbidden，阻断首次配置。
// 依赖：调用方须先 SeedBuiltinRoles（wire.go 顺序：roles 先 admin 后，已满足）。
package auth

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
)

// OrgAdminRoleName 内置组织超管角色名（与 seed.go 的 builtinRoles 一致）。
const OrgAdminRoleName = "org_admin"

// SeedDefaultAdmin 若无 admin 用户则创建默认管理员（username=admin, password=changeme），
// 并绑定 org_admin 角色（org scope，拥有全部权限）。
// 返回 (created, err)：created=true 表示本次新建了 admin（需提醒改密）。
// 幂等：已有 admin 则 created=false，无副作用（不重复绑角色）。
// 新建的 admin 标记 must_change_password=true，强制首登改密。
func SeedDefaultAdmin(ctx context.Context, db *ent.Client) (created bool, err error) {
	u, err := db.User.Create().
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
	// FIX-A：新建 admin 后绑定 org_admin 角色，否则 admin 无权限无法首次配置。
	// org_admin 角色由 SeedBuiltinRoles 创建（须在调用本函数前完成）。
	if err := bindOrgAdmin(ctx, db, u.ID); err != nil {
		// 绑定失败不阻断启动（admin 仍可登录改密），但记录错误供排查。
		// 极端情况（org_admin 角色未 seed）admin 仍无权限，但启动日志会反映。
		return true, fmt.Errorf("seed admin: bind org_admin: %w", err)
	}
	return true, nil
}

// bindOrgAdmin 把 userID 绑定到 org_admin 角色（org scope）。
// 幂等：已绑定则跳过（ConstraintError）。
func bindOrgAdmin(ctx context.Context, db *ent.Client, userID int) error {
	r, err := db.Role.Query().Where(role.NameEQ(OrgAdminRoleName)).Only(ctx)
	if err != nil {
		return fmt.Errorf("query %s role (run SeedBuiltinRoles first): %w", OrgAdminRoleName, err)
	}
	_, err = db.RoleBinding.Create().
		SetUserID(userID).
		SetRoleID(r.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).
		Save(ctx)
	if err != nil && !ent.IsConstraintError(err) {
		return fmt.Errorf("create role binding: %w", err)
	}
	return nil
}
