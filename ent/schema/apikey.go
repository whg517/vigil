package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// APIKey 程序化接入凭证（能力域 13 §API Key 管理，PRD M13.7）。
//
// 设计：
//   - 明文 token 仅在创建时返回一次，库内只存 SHA256 哈希（token_hash）
//   - prefix 存明文前缀（如 vgl_a1b2），列表展示用，便于用户识别哪个 key
//   - 归属 User（IdentityResolver 解析出 user_id 后，鉴权仍走该 User 的 RoleBinding）
//   - scope 预留权限收敛（本期不强制，鉴权继承 User 角色；后续可细化到 key 级权限）
//   - status/expires_at/last_used_at 控制生命周期
type APIKey struct {
	ent.Schema
}

func (APIKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Comment("key 名称，便于识别用途"),
		// token_hash SHA256(明文)，库内只存哈希。Sensitive 防日志/序列化泄露（虽已是哈希）。
		field.String("token_hash").Sensitive().Comment("token 的 SHA256 哈希"),
		// prefix 明文前缀（前 12 字符），列表展示用，不泄露完整 token
		field.String("prefix").Comment("明文前缀，列表展示识别用"),
		// scope 权限范围 code 列表（预留，本期鉴权继承 User 角色，见 IdentityResolver）
		field.JSON("scope", []string{}).Optional().Comment("权限范围（预留，本期继承 User 角色）"),
		field.Time("expires_at").Optional().Nillable().Comment("过期时间，空=永久"),
		field.Time("last_used_at").Optional().Nillable().Comment("最后使用时间，校验时更新"),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (APIKey) Edges() []ent.Edge {
	return []ent.Edge{
		// APIKey <- User（归属用户，User.api_keys 的反向）
		edge.From("user", User.Type).Ref("api_keys").Unique().Required(),
	}
}

func (APIKey) Indexes() []ent.Index {
	return []ent.Index{
		// token_hash 查询高频（每次 API Key 鉴权都查）
		index.Fields("token_hash").Unique(),
		index.Fields("status"),
	}
}
