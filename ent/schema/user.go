package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User 用户 —— oncall 响应者/管理者。
// 设计见 ADR-0027（RBAC 主体）与 ADR-0028（软隔离）。
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
		// 注：向后兼容保留；新绑定会同时写入 IMAccountBinding 独立表（可索引查询）。
		field.JSON("im_accounts", []IMAccount{}).Optional().Comment("IM 账号绑定（JSON，兼容字段；新数据见 im_bindings 表）"),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.String("timezone").Default("Asia/Shanghai"),
		// password_hash 本地密码 bcrypt 哈希（能力域 13 JWT 登录态）。
		// Sensitive：ent 序列化/日志时自动脱敏，避免泄露。
		// 仅 JWT 登录链路使用；IM/SSO 绑定不写此字段。空=未设密码，拒绝密码登录。
		field.String("password_hash").Sensitive().Optional().Comment("密码哈希（bcrypt），仅登录链路用"),
		// must_change_password 强制改密标志（H1.6 默认安全）：
		// 默认管理员 seed 时置 true，登录后必须改密才能访问业务 API，杜绝 admin/changeme 长期可用。
		field.Bool("must_change_password").Default(false).Comment("强制改密标志（默认 admin seed 置 true）"),
		// token_version 令牌版本号（T0.4 改密令牌吊销）：
		// 每次改密自增 1，签发 JWT 时写入 claims，鉴权时与库中当前值比对——不一致即视为已吊销。
		// 无状态 JWT 无法主动作废；靠版本号让"改密"这一动作立即使所有旧 access/refresh token 失效
		//（防旧密码泄露后攻击者持既有 token 长期访问）。
		field.Int("token_version").Default(0).Comment("令牌版本号（改密自增，旧 token 失效凭据）"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// IMAccount IM 平台账号绑定（User.im_accounts 元素）。
// 一个 User 可绑多个 IM 平台账号，是 IM-first 的前提。
type IMAccount struct {
	Platform  string `json:"platform"`   // dingtalk | feishu
	AccountID string `json:"account_id"` // IM 平台的 unionId
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		// User <-> Team 多对多（团队拥有成员，反向）
		edge.From("teams", Team.Type).Ref("users"),
		// User -> RoleBinding（被授权的角色绑定）
		edge.To("role_bindings", RoleBinding.Type),
		// User -> IMAccountBinding（IM 平台账号绑定，可索引查询的独立表）
		edge.To("im_bindings", IMAccountBinding.Type),
		// User -> APIKey（程序化接入凭证，APIKey.user 的反向）
		edge.To("api_keys", APIKey.Type),
		// User <- Incident（作为当前责任人，Incident.assignee 的反向）
		edge.From("assigned_incidents", Incident.Type).Ref("assignee"),
		// User <- Incident（作为响应者，Incident.responders 的反向）
		edge.From("responding_incidents", Incident.Type).Ref("responders"),
		// User <- Rotation（参与排班轮换）
		edge.From("rotations", Rotation.Type).Ref("participants"),
		// User -> Subscription（定向订阅关系，T4.4）
		edge.To("subscriptions", Subscription.Type),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
