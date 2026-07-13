package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WebhookDelivery 出站 webhook 投递记录 —— 死信/可观测底座（T5.2，S13/C24）。
//
// 对应出向 webhook（ADR-0030 四方向集成）：
// Vigil 把 incident 生命周期事件 POST 给订阅 URL。原实现「全部重试失败仅记 metric 后静默丢弃」，
// 无从得知哪条事件没送达、更无法补投——这在告警平台是不可接受的可靠性盲区。
//
// 本实体记录每次出站投递的终态：成功也落一条（便于审计/统计），失败落 failed（即死信），
// 可查（GET /webhook-deliveries?status=failed）、可重放（POST /webhook-deliveries/:id/replay）。
//
// 归属取舍：出站 URL 是「配置式全局订阅」（VIGIL_WEBHOOK_OUT_URLS，非 team 资源），
// 故 WebhookDelivery 不挂 team 边——查询/重放是 org 级运维操作，用 org 级权限点闸门
// （webhook_delivery.view / webhook_delivery.replay），不走 team 软隔离。
//
// payload 全量存储：重放需要原样重发同一 body（含签名基串），故必须留存。
// 出站 payload 是 incident 生命周期摘要（title/severity/status 等），不含凭据/密钥，可安全留存。
type WebhookDelivery struct {
	ent.Schema
}

func (WebhookDelivery) Fields() []ent.Field {
	return []ent.Field{
		// url 目标订阅 URL（哪一个订阅端）。
		field.String("url").NotEmpty().Comment("目标订阅 URL"),
		// event 事件名（如 incident.created），冗余存便于按事件类型筛/查。
		field.String("event").Comment("事件名，如 incident.created"),
		// incident_id 关联 incident（冗余存，便于按单查投递；非外键——incident 可能已归档删除）。
		field.Int("incident_id").Optional().Comment("关联 incident id（冗余，非外键）"),
		// payload 出站请求体全量（重放需原样重发，且是签名基串）。
		field.Bytes("payload").Comment("出站请求体全量（重放原样重发）"),
		// status 投递终态。
		//   - success  最终送达（2xx）
		//   - failed   重试耗尽仍失败（死信，可重放）
		field.Enum("status").
			Values("success", "failed").
			Default("failed").
			Comment("投递终态：success 已送达 / failed 死信可重放"),
		// attempts 累计投递尝试次数（含重放追加，便于判断是否值得继续重放）。
		field.Int("attempts").Default(0).Comment("累计投递尝试次数"),
		// last_error 最后一次失败原因（排障用；success 时为空）。
		field.String("last_error").Optional().Comment("最后失败原因（success 时为空）"),
		// last_status_code 最后一次响应状态码（0=连接失败未拿到响应）。
		field.Int("last_status_code").Optional().Comment("最后响应状态码，0=连接失败"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// WebhookDelivery 无边（不挂 team，见类型注释——出站 URL 是全局配置式订阅）。
func (WebhookDelivery) Edges() []ent.Edge { return nil }

func (WebhookDelivery) Indexes() []ent.Index {
	return []ent.Index{
		// 按状态查死信（GET /webhook-deliveries?status=failed）高频。
		index.Fields("status"),
		// 按 incident 查某单的全部出站投递。
		index.Fields("incident_id"),
	}
}
