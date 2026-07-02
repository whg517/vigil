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
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/predicate"
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
// 单次查询该用户所有有效 RoleBinding（SQL 端过滤 user + 过期 + scope），
// 合并权限集后内存判定，避免对每个权限点重复查询。
func (a *Authorizer) Check(ctx context.Context, req AuthzRequest) (bool, error) {
	permSet, err := a.effectivePermissions(ctx, req.UserID, req.TeamScope)
	if err != nil {
		return false, err
	}
	return permSet[req.Permission], nil
}

// CheckAny 批量检查：返回用户在该 scope 下拥有的权限子集（供卡片按权限渲染按钮）。
// 单次查询合并权限集后内存判定全部 perm，不再对每个 perm 各查一次（消除 N 次全表扫描）。
func (a *Authorizer) CheckAny(ctx context.Context, userID int, teamScope *int, perms []Permission) (map[Permission]bool, error) {
	permSet, err := a.effectivePermissions(ctx, userID, teamScope)
	if err != nil {
		return nil, err
	}
	result := make(map[Permission]bool, len(perms))
	for _, p := range perms {
		result[p] = permSet[p]
	}
	return result, nil
}

// VisibleTeamIDs 返回用户可见的 team ID 集合（ARCH-02/SEC-01，list 数据隔离用）。
//
// 返回 (teamIDs, orgWide, err)：
//   - orgWide=true：用户有任一有效的 org 级 RoleBinding → 可见全部 team（list 不过滤）
//   - orgWide=false：用户仅有 team 级 binding → 返回这些 binding 涉及的 team_id（list 限定）
//   - orgWide=false 且 teamIDs 为空：用户无任何有效 binding（list 应返回空）
//
// 设计：list 查询无法用 path :team_id（列表路由无该参数），改用此方法在查询前
// 取得"用户可见域"，注入到 Where(team.IDIn(...)) 过滤。
func (a *Authorizer) VisibleTeamIDs(ctx context.Context, userID int) (teamIDs []int, orgWide bool, err error) {
	now := time.Now()
	bindings, err := a.db.RoleBinding.Query().
		Where(
			rolebinding.HasUserWith(user.IDEQ(userID)),
			// 未过期
			rolebinding.Or(rolebinding.ExpiresAtIsNil(), rolebinding.ExpiresAtGTE(now)),
		).
		All(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("query role bindings for visible teams: %w", err)
	}
	seen := make(map[int]bool)
	for _, b := range bindings {
		if b.ScopeLevel == rolebinding.ScopeLevelOrg {
			return nil, true, nil // 有 org 级 binding → 全可见
		}
		if b.ScopeLevel == rolebinding.ScopeLevelTeam && b.TeamID != "" {
			// team 级 binding 的 TeamID 存为字符串（见 effectivePermissions 用法）
			if id, perr := strconv.Atoi(b.TeamID); perr == nil && id > 0 && !seen[id] {
				teamIDs = append(teamIDs, id)
				seen[id] = true
			}
		}
	}
	return teamIDs, false, nil
}

// effectivePermissions 一次性查询用户在指定 scope 下所有有效 RoleBinding，
// 合并它们的权限点为集合返回。
//
// SQL 端过滤（减少传输与内存遍历）：
//   - user 匹配；
//   - 未过期：expires_at 为 nil 或 >= now；
//   - scope 生效：org 级始终生效；team 级需匹配 teamScope。
//
// 返回的 map 作为 Check/CheckAny 的内存判定基础，O(1) 命中。
func (a *Authorizer) effectivePermissions(ctx context.Context, userID int, teamScope *int) (map[Permission]bool, error) {
	now := time.Now()
	preds := []predicate.RoleBinding{
		rolebinding.HasUserWith(user.IDEQ(userID)),
		// 未过期：expires_at 为 nil 或在未来
		rolebinding.Or(rolebinding.ExpiresAtIsNil(), rolebinding.ExpiresAtGTE(now)),
	}

	// scope 过滤：org 级始终生效，team 级仅当与 teamScope 匹配。
	// 无 teamScope 时只取 org 级（team 级 binding 不生效）。
	scopePreds := []predicate.RoleBinding{
		rolebinding.ScopeLevelEQ(rolebinding.ScopeLevelOrg),
	}
	if teamScope != nil {
		teamIDStr := fmt.Sprintf("%d", *teamScope)
		scopePreds = append(scopePreds, rolebinding.And(
			rolebinding.ScopeLevelEQ(rolebinding.ScopeLevelTeam),
			rolebinding.TeamIDEQ(teamIDStr),
		))
	}
	preds = append(preds, rolebinding.Or(scopePreds...))

	bindings, err := a.db.RoleBinding.Query().
		Where(preds...).
		WithRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query role bindings: %w", err)
	}

	// 合并所有有效 binding 的 Role 权限点（并集）
	permSet := make(map[Permission]bool)
	for _, b := range bindings {
		rl := b.Edges.Role
		if rl == nil {
			continue
		}
		for _, p := range rl.Permissions {
			permSet[Permission(p)] = true
		}
	}
	return permSet, nil
}
