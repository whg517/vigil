package im

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/schema"
)

// Mapper 把 IM 平台账号（platform + unionID）映射回 Vigil User。
// 对应 capabilities/05-im-chatops.md §6：User.im_accounts 是映射的桥梁。
// im_accounts 是 JSON 字段，ent 不支持 JSON 内查询，故全表扫后内存匹配
// （与 authz 现有的 JSON 过滤策略一致；用户量级内可接受，后续可加索引表优化）。
type Mapper struct {
	db *ent.Client
}

// NewMapper 创建映射器。
func NewMapper(db *ent.Client) *Mapper {
	return &Mapper{db: db}
}

// ResolveUser 按 platform + unionID 查找绑定的 User。
// 未绑定返回 ErrNotBound（调用方据此提示去 Web 绑定）。
func (m *Mapper) ResolveUser(ctx context.Context, platform, unionID string) (*ent.User, error) {
	if platform == "" || unionID == "" {
		return nil, ErrNotBound
	}
	users, err := m.db.User.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	for _, u := range users {
		for _, acc := range u.ImAccounts {
			if acc.Platform == platform && acc.AccountID == unionID {
				return u, nil
			}
		}
	}
	return nil, ErrNotBound
}

// BindAccount 给 User 绑定一个 IM 平台账号（幂等：同 platform+account 不重复加）。
// 供 Web 端「绑定 IM」接口调用。
func (m *Mapper) BindAccount(ctx context.Context, userID int, platform, unionID string) error {
	u, err := m.db.User.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	accounts := u.ImAccounts
	for _, acc := range accounts {
		if acc.Platform == platform && acc.AccountID == unionID {
			return nil // 已绑定，幂等
		}
	}
	accounts = append(accounts, schema.IMAccount{Platform: platform, AccountID: unionID})
	return m.db.User.UpdateOneID(userID).SetImAccounts(accounts).Exec(ctx)
}
