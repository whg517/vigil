package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User 用户 —— oncall 响应者/管理者。
// 对应 data-model.md §3.1 User。
type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").Unique().NotEmpty().Comment("登录名"),
		field.String("name").Optional().Comment("显示名"),
		field.String("email").Unique().Comment("邮箱"),
		field.String("phone").Optional().Comment("电话，用于 SMS/语音"),
		// im_accounts 以 JSON 存多 IM 平台账号绑定（钉钉/飞书/企微）
		field.JSON("im_accounts", []IMAccount{}).Optional().Comment("IM 账号绑定，支持多平台"),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.String("timezone").Default("Asia/Shanghai"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// IMAccount IM 平台账号绑定（User.im_accounts 元素）。
// 一个 User 可绑多个 IM 平台账号，是 IM-first 的前提。
type IMAccount struct {
	Platform  string `json:"platform"`   // dingtalk | feishu | wecom
	AccountID string `json:"account_id"` // IM 平台的 unionId
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		// User <-> Team 多对多（团队拥有成员，反向）
		edge.From("teams", Team.Type).Ref("users"),
		// User -> RoleBinding（被授权的角色绑定）
		edge.To("role_bindings", RoleBinding.Type),
		// User <- Incident（作为当前责任人，Incident.assignee 的反向）
		edge.From("assigned_incidents", Incident.Type).Ref("assignee"),
		// User <- Incident（作为响应者，Incident.responders 的反向）
		edge.From("responding_incidents", Incident.Type).Ref("responders"),
		// User <- Rotation（参与排班轮换）
		edge.From("rotations", Rotation.Type).Ref("participants"),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
