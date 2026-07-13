package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TicketIntegration 出向工单集成配置 —— 复盘 ActionItem 自动建单的目标系统。
//
// 对应出向集成「工单系统」（ADR-0030 四方向集成）：
// 复盘发布时把 ActionItem 推到外部工单系统（Jira/禅道/通用 webhook）建改进任务，回写 tracker_url。
//
// 与入向 Integration（告警源接入）分开建实体：语义正交（一个进告警、一个出工单），
// 凭据与配置结构不同，混在一张表会让 config/token 字段语义含糊。归属沿用 Team 软隔离边界，
// team_id 为空视为 org 级（全组织可用），与 Integration 同款。
type TicketIntegration struct {
	ent.Schema
}

func (TicketIntegration) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// type 决定用哪个适配器建单。只有通用 webhook 工单——具体厂商 SDK 明确不做，
		// 需要对接 Jira/禅道等系统时经用户侧 webhook 网关转换。
		field.Enum("type").Values("webhook").Default("webhook"),
		// endpoint 目标系统建单 URL。
		field.String("endpoint").NotEmpty().Comment("建单目标 URL"),
		// credential 凭据（API token / 密码等），Sensitive 加密存储、list/get 不回显。
		// 复用 Integration.token 的 Sensitive 模式（见 service.go Integration）。
		field.String("credential").Sensitive().Optional().Comment("建单凭据（token/密码），加密存储不回显"),
		// callback_secret 工单侧状态回调（N1.3）的共享密钥（HMAC-SHA256 验签）。
		// 外部工单系统关闭/推进工单时回调 Vigil 端点，用此密钥对 body 签名，Vigil 验签防伪造。
		// 与 credential 同为 Sensitive（加密存储、不回显）。空则该集成不接受回调（拒绝）。
		field.String("callback_secret").Sensitive().Optional().Comment("工单状态回调验签密钥（HMAC），加密存储不回显"),
		// config 类型相关配置：目标项目 key、issue type、字段映射（owner→assignee 等）。
		field.JSON("config", map[string]any{}).Optional().Comment("目标项目/字段映射等类型相关配置"),
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TicketIntegration) Edges() []ent.Edge {
	return []ent.Edge{
		// TicketIntegration <- Team（归属团队，nil 视为 org 级）。
		edge.From("team", Team.Type).Ref("ticket_integrations").Unique(),
	}
}

func (TicketIntegration) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
	}
}
