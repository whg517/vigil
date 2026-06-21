package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// IMAccountBinding IM 平台账号绑定 —— platform+account_id → user 的独立索引表。
//
// 设计动机：User.im_accounts（JSON）无法在 DB 端按 platform/account_id 查询，
// 原 im.Mapper.ResolveUser 全表扫描后内存匹配，IM 回调高频路径下成为瓶颈。
// 此独立表提供 O(1) 索引查询，是 IM-first 的账号映射桥梁。
//
// 与 User.im_accounts JSON 字段并存（向后兼容，逐步迁移）：
// BindAccount 双写，ResolveUser 优先查此表。
type IMAccountBinding struct {
	ent.Schema
}

func (IMAccountBinding) Fields() []ent.Field {
	return []ent.Field{
		// platform IM 平台标识：feishu | dingtalk | wecom
		field.Enum("platform").Values("feishu", "dingtalk", "wecom"),
		// account_id IM 平台返回的稳定账号标识（飞书 open_id / 钉钉 unionId / 企微 userid）
		field.String("account_id").NotEmpty().Comment("IM 平台账号标识（open_id/unionId/userid）"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (IMAccountBinding) Edges() []ent.Edge {
	return []ent.Edge{
		// IMAccountBinding <- User（归属用户，多对一：一个 User 可绑多个平台账号）
		edge.From("user", User.Type).Ref("im_bindings").Unique().Required(),
	}
}

func (IMAccountBinding) Indexes() []ent.Index {
	return []ent.Index{
		// (platform, account_id) 全局唯一：同一平台同一账号只能绑一个 User
		// 这是账号映射正确性的核心约束（一个 IM 账号对应一个 Vigil User）
		index.Fields("platform", "account_id").Unique(),
	}
}
