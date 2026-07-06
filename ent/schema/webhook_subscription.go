package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WebhookSubscription 出站 webhook 动态订阅（N2.2）——替代/补充环境变量 VIGIL_WEBHOOK_OUT_URLS。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A4 出向 webhook：
// 用户订阅 incident 生命周期事件（created/acked/resolved/escalated/reopened/closed/responder_added/merged），
// Vigil 在事件发生时按订阅的事件类型过滤后推送到订阅 URL。
//
// 背景：原实现出站目标 URL 只能靠 VIGIL_WEBHOOK_OUT_URLS 环境变量配（全局静态、需重启改），
// 无法运行时按需增删/按团队隔离/按事件类型过滤/按订阅独立签名。本实体提供运行时 CRUD 的
// 动态订阅，dispatcher 出站时把 env 静态订阅与 DB 动态订阅合并（向后兼容 env）。
//
// 归属取舍：与 TicketIntegration/Credential 同款，team_id 为空视为 org 级（全组织事件都投递）；
// 有 team 归属则只投递该团队相关的事件（当前 incident 出站 payload 不带 team 过滤——见 dispatcher
// 说明，团队级订阅仍收全量事件，team 字段主要用于 list 数据隔离与管理归属，事件级 team 过滤留后续）。
//
// 签名：复用 T5.2 的 HMAC-SHA256 出站签名（webhook.Sign）。每订阅可配独立 signing_secret，
// 非空则出站时用该密钥签名（Sensitive 加密语义，list/get 不回显），空则该订阅出站不签名。
type WebhookSubscription struct {
	ent.Schema
}

func (WebhookSubscription) Fields() []ent.Field {
	return []ent.Field{
		// name 人类可读名（管理面识别用），非唯一。
		field.String("name").Optional().Comment("订阅名（管理识别用）"),
		// url 出站目标 URL（事件发生时 POST 到这里）。
		field.String("url").NotEmpty().Comment("出站目标 URL"),
		// event_types 订阅的事件类型过滤（如 ["incident.created","incident.resolved"]）。
		// 空=订阅所有事件类型（向后兼容 env 全量投递语义）。
		field.JSON("event_types", []string{}).Optional().Comment("订阅的事件类型，空=所有"),
		// signing_secret 每订阅独立的出站签名密钥（HMAC-SHA256）。Sensitive：list/get 不回显。
		// 空=该订阅出站不签名（向后兼容既有订阅端）。
		field.String("signing_secret").Sensitive().Optional().Comment("出站 HMAC 签名密钥（不回显），空=不签名"),
		// enabled 是否启用（false=暂停投递，不删除配置）。
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (WebhookSubscription) Edges() []ent.Edge {
	return []ent.Edge{
		// WebhookSubscription <- Team（归属团队，nil 视为 org 级）。
		edge.From("team", Team.Type).Ref("webhook_subscriptions").Unique(),
	}
}

func (WebhookSubscription) Indexes() []ent.Index {
	return []ent.Index{
		// 出站分发时按 enabled 查活跃订阅（高频）。
		index.Fields("enabled"),
	}
}
