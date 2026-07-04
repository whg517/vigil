package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Credential 加密托管的凭据 —— Runbook 执行器/外接系统访问外部平台用的密钥（T6.3）。
//
// 对应 docs/capabilities/06-runbook.md §7 Q1「执行器的凭证管理（Ansible/Jenkins token）→
// 加密存储于 Vigil，admin 管理」。审计 S16：此前 Runbook step 只能把 token 明文写进
// endpoint/params，泄露风险高；现改为独立托管凭据，step 引用凭据 id（credential_ref），
// 执行时解密注入 Authorization 头，明文绝不落 step/日志/时间线。
//
// 密文存储：secret_ciphertext 存 AES-256-GCM 密文（base64），密钥从环境变量注入
// （见 internal/crypto）。字段加 .Sensitive() → json:"-"，API 响应恒不回显（读取只返元数据）。
// ★ 即便某处误打印实体，Sensitive 也让 String() 输出 <sensitive>，密文本身也不含明文。
//
// 归属沿用 Team 软隔离边界：team_id 为空视为 org 级（全组织可用），与 Integration/
// TicketIntegration 同款。
type Credential struct {
	ent.Schema
}

func (Credential) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Comment("凭据名（引用标识，如 jenkins-prod-token）"),
		// type 提示凭据注入方式，供执行器决定如何用：
		//   - bearer：注入 Authorization: Bearer <secret>（默认，覆盖多数 token 场景）
		//   - token ：注入 Authorization: <secret>（原样，含用户自带 scheme 前缀如 "Token xxx"）
		//   - basic ：注入 Authorization: Basic <secret>（secret 应为 base64(user:pass)）
		//   - header：注入自定义头（头名取 config.header，值为 <secret>）
		field.Enum("type").Values("bearer", "token", "basic", "header").Default("bearer").
			Comment("注入方式：bearer/token/basic/header"),
		// secret_ciphertext 凭据密文（AES-256-GCM，base64）。Sensitive → 不回显、日志脱敏。
		// 明文只在加密前/解密后短暂存在于内存，绝不落库/日志。
		field.String("secret_ciphertext").Sensitive().NotEmpty().
			Comment("凭据密文（AES-256-GCM base64），加密存储不回显"),
		// config 类型相关配置：header 类型用 config.header 指定头名；其余类型可空。
		field.JSON("config", map[string]any{}).Optional().
			Comment("类型相关配置（如 header 类型的头名）"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Credential) Edges() []ent.Edge {
	return []ent.Edge{
		// Credential <- Team（归属团队，nil 视为 org 级）。
		edge.From("team", Team.Type).Ref("credentials").Unique(),
	}
}

func (Credential) Indexes() []ent.Index {
	return []ent.Index{
		// 同一归属下凭据名唯一（org 级 team 为 NULL，靠应用层 + 名去重；此处仅加名索引便于查询）。
		index.Fields("name"),
	}
}
