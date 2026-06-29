package im

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/imaccountbinding"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/user"
)

// Mapper 把 IM 平台账号（platform + unionID）映射回 Vigil User。
//
// 对应 capabilities/05-im-chatops.md §6：IM 账号 → User 是 IM 操作鉴权的桥梁。
//
// 实现演进：
//   - 旧：全表扫描 User.im_accounts（JSON）后内存匹配——高频回调路径瓶颈。
//   - 新：优先查 IMAccountBinding 独立表（platform+account_id 唯一索引，O(1) 查询），
//     兜底回退到 JSON 字段扫描（兼容历史数据）。
//
// BindAccount 双写（独立表 + JSON 字段），保证两条路径都能解析。
type Mapper struct {
	db *ent.Client
}

// NewMapper 创建映射器。
func NewMapper(db *ent.Client) *Mapper {
	return &Mapper{db: db}
}

// ResolveUser 按 platform + unionID 查找绑定的 User。
// 优先走 IMAccountBinding 索引表；未命中再回退 JSON 字段扫描（兼容旧数据）。
// 未绑定返回 ErrNotBound（调用方据此提示去 Web 绑定）。
func (m *Mapper) ResolveUser(ctx context.Context, platform, unionID string) (*ent.User, error) {
	if platform == "" || unionID == "" {
		return nil, ErrNotBound
	}
	// 1. 优先查独立表（O(1) 索引）
	binding, err := m.db.IMAccountBinding.Query().
		Where(
			imaccountbinding.PlatformEQ(imaccountbinding.Platform(platform)),
			imaccountbinding.AccountIDEQ(unionID),
		).
		WithUser().
		Only(ctx)
	if err == nil && binding != nil && binding.Edges.User != nil {
		return binding.Edges.User, nil
	}
	// 2. 回退：JSON 字段扫描（兼容历史数据，仅独立表无记录时）
	if u, ok := m.resolveViaJSON(ctx, platform, unionID); ok {
		return u, nil
	}
	return nil, ErrNotBound
}

// resolveViaJSON 全表扫 User.im_accounts JSON 字段匹配（兼容旧数据路径）。
// 用户量级大时较慢，仅作独立表未命中的兜底。
func (m *Mapper) resolveViaJSON(ctx context.Context, platform, unionID string) (*ent.User, bool) {
	users, err := m.db.User.Query().All(ctx)
	if err != nil {
		return nil, false
	}
	for _, u := range users {
		for _, acc := range u.ImAccounts {
			if acc.Platform == platform && acc.AccountID == unionID {
				return u, true
			}
		}
	}
	return nil, false
}

// BindAccount 给 User 绑定一个 IM 平台账号（幂等：同 platform+account 不重复加）。
// 双写：独立表 IMAccountBinding（可索引查询）+ User.im_accounts JSON（向后兼容）。
// 供 Web 端「绑定 IM」接口调用。
func (m *Mapper) BindAccount(ctx context.Context, userID int, platform, unionID string) error {
	// 1. 独立表：幂等插入（platform+account_id 唯一约束兜底竞态）
	exists, err := m.db.IMAccountBinding.Query().
		Where(
			imaccountbinding.PlatformEQ(imaccountbinding.Platform(platform)),
			imaccountbinding.AccountIDEQ(unionID),
		).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("query im binding: %w", err)
	}
	if exists == 0 {
		if _, err := m.db.IMAccountBinding.Create().
			SetPlatform(imaccountbinding.Platform(platform)).
			SetAccountID(unionID).
			SetUserID(userID).
			Save(ctx); err != nil {
			// 唯一约束冲突 = 并发已插入，视为幂等成功
			if !ent.IsConstraintError(err) {
				return fmt.Errorf("create im binding: %w", err)
			}
		}
	}

	// 2. JSON 字段：幂等追加（兼容旧路径）
	u, err := m.db.User.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	for _, acc := range u.ImAccounts {
		if acc.Platform == platform && acc.AccountID == unionID {
			return nil // JSON 字段已绑定，幂等
		}
	}
	accounts := append(u.ImAccounts, schema.IMAccount{Platform: platform, AccountID: unionID})
	return m.db.User.UpdateOneID(userID).SetImAccounts(accounts).Exec(ctx)
}

// ListBindings 列出用户已绑定的全部 IM 账号（QA 审计 C6，供 UserHandler 查询）。
// 优先查独立表；表无记录时回退 User.im_accounts JSON 字段。
func (m *Mapper) ListBindings(ctx context.Context, userID int) ([]IMBindingView, error) {
	bindings, err := m.db.IMAccountBinding.Query().
		Where(imaccountbinding.HasUserWith(user.IDEQ(userID))).
		All(ctx)
	if err == nil && len(bindings) > 0 {
		out := make([]IMBindingView, 0, len(bindings))
		for _, b := range bindings {
			out = append(out, IMBindingView{Platform: string(b.Platform), AccountID: b.AccountID})
		}
		return out, nil
	}
	// 回退 JSON 字段
	u, err := m.db.User.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]IMBindingView, 0, len(u.ImAccounts))
	for _, a := range u.ImAccounts {
		out = append(out, IMBindingView{Platform: a.Platform, AccountID: a.AccountID})
	}
	return out, nil
}

// IMBindingView IM 账号绑定视图（脱敏，供 handler 返回）。
type IMBindingView struct {
	Platform  string
	AccountID string
}
