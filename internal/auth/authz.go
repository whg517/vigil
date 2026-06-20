// authz.go 实现 RBAC 鉴权器（能力域 13 核心）。
//
// 对应 docs/data-model.md §5.5 鉴权流程：
//
//	操作请求 (user, action, resource)
//	  → 解析 action 得 permission_code
//	  → 解析 resource 得 scope（如 incident.team_id）
//	  → 查 user 在 org + team scope 的所有 RoleBinding
//	  → 合并这些 RoleBinding 的权限点
//	  → 判定 permission_code ∈ 权限集
//
// 权限合并规则：org 级和 team 级取并集（任一授予即生效）。
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/user"
)

// Authorizer RBAC 鉴权器。
type Authorizer struct {
	db *ent.Client
}

// NewAuthorizer 创建鉴权器。
func NewAuthorizer(db *ent.Client) *Authorizer {
	return &Authorizer{db: db}
}

// AuthzRequest 鉴权请求。
type AuthzRequest struct {
	UserID     int
	Permission Permission // 要检查的权限点
	TeamScope  *int       // 资源所属团队（nil=仅查 org 级）
}

// Check 检查用户是否拥有某权限。
// 合并该用户在 org 级 + team 级（指定团队）的所有 RoleBinding 的权限点，
// 任一授予即通过。考虑 expires_at（过期的不计）。
func (a *Authorizer) Check(ctx context.Context, req AuthzRequest) (bool, error) {
	// 查该用户所有有效（未过期）的 RoleBinding，带 Role
	q := a.db.RoleBinding.Query().
		Where(rolebinding.HasUserWith(user.IDEQ(req.UserID))).
		WithRole()

	// 过滤过期的（expires_at 为 null 或在未来）
	// ent 的 nillable time 过滤：用 HasExpiresAt + GTE，或用 SQL。
	// 简化：查全部后在内存过滤（数据量小，可接受）
	bindings, err := q.All(ctx)
	if err != nil {
		return false, fmt.Errorf("query role bindings: %w", err)
	}

	for _, b := range bindings {
		// 过期检查
		if b.ExpiresAt != nil && b.ExpiresAt.Before(time.Now()) {
			continue
		}
		// scope 检查：org 级始终生效；team 级仅当与 req.TeamScope 匹配
		if b.ScopeLevel == rolebinding.ScopeLevelTeam {
			if req.TeamScope == nil {
				continue // 请求无团队 scope，team 级绑定不生效
			}
			// b.TeamID 是字符串，req.TeamScope 是 *int，比较需转换
			teamIDStr := fmt.Sprintf("%d", *req.TeamScope)
			if b.TeamID != teamIDStr {
				continue // 不同团队，不生效
			}
		}
		// 检查该 Role 的权限点是否包含 req.Permission
		rl := b.Edges.Role
		if rl == nil {
			continue
		}
		if hasPermission(rl.Permissions, req.Permission) {
			return true, nil
		}
	}
	return false, nil
}

// CheckAny 批量检查：返回用户在该 scope 下拥有的权限子集（供卡片按权限渲染按钮）。
func (a *Authorizer) CheckAny(ctx context.Context, userID int, teamScope *int, perms []Permission) (map[Permission]bool, error) {
	result := make(map[Permission]bool, len(perms))
	for _, p := range perms {
		ok, err := a.Check(ctx, AuthzRequest{UserID: userID, Permission: p, TeamScope: teamScope})
		if err != nil {
			return nil, err
		}
		result[p] = ok
	}
	return result, nil
}

// hasPermission 检查权限点列表是否包含某权限。
func hasPermission(perms []string, want Permission) bool {
	for _, p := range perms {
		if Permission(p) == want {
			return true
		}
	}
	return false
}
