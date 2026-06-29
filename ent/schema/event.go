package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RawEvent 原始告警暂存 —— "先落库再处理"的可靠性保证。
// 对应能力域 01 §7.1（data-model 未显式定义，本设计新增）。
// Receiver 先落 RawEvent，再入队归一化；保证任何情况下告警不丢。
type RawEvent struct {
	ent.Schema
}

func (RawEvent) Fields() []ent.Field {
	return []ent.Field{
		// payload 原始字节，保证可重放
		field.Bytes("payload").Comment("原始 payload"),
		field.JSON("headers", map[string]string{}).Optional(),
		field.Time("received_at").Default(time.Now).Immutable(),
		field.Enum("status").Values(
			"received", "normalized", "parse_failed", "requeued",
		).Default("received"),
		field.Text("error").Optional().Comment("失败原因"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (RawEvent) Edges() []ent.Edge {
	return []ent.Edge{
		// RawEvent <- Integration（来源接入点）
		edge.From("integration", Integration.Type).Ref("raw_events").Unique(),
	}
}

func (RawEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "received_at"),
	}
}

// Event 原始告警信号 —— 归一化后的内部事件模型。
// 对应 data-model.md §3.3 Event。
// 不可变的历史记录，只追加。
type Event struct {
	ent.Schema
}

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.String("source_event_id").Comment("原始告警 ID，去重 + 幂等键"),
		field.String("source").Comment("告警源，如 prometheus"),
		field.Enum("severity").Values("critical", "warning", "info"),
		field.Enum("status").Values("firing", "resolved"),
		field.String("summary").Comment("一句话摘要"),
		// detail 原始 payload 归一化后的明细，JSONB
		field.JSON("detail", map[string]any{}).Optional().Comment("原始 payload 明细"),
		// labels 路由用标签
		field.JSON("labels", map[string]string{}).Optional().Comment("路由用标签"),
		// dedup_key 去重键
		field.String("dedup_key").Comment("去重键"),
		field.Time("received_at").Default(time.Now).Immutable(),
		field.Bool("is_noise").Default(false).Comment("分诊判定为噪音"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Event) Edges() []ent.Edge {
	return []ent.Edge{
		// Event <- Integration（来源接入点）
		edge.From("integration", Integration.Type).Ref("events").Unique(),
		// Event <- Service（路由命中的服务，可空=unrouted）
		edge.From("service", Service.Type).Ref("events").Unique(),
		// Event -> Incident（聚合到的 Incident，可空）
		edge.To("incident", Incident.Type).Unique(),
	}
}

func (Event) Indexes() []ent.Index {
	return []ent.Index{
		// 去重/幂等查询高频
		index.Fields("dedup_key"),
		// 幂等唯一键（QA 审计 C2）：含 status 列。
		// 旧索引 (source, source_event_id) 导致 firing 与 resolved 共用同一 fingerprint
		// 时，resolved 落库撞唯一约束被静默丢弃 → Incident 永不自动解决（M3.7 失效）。
		// 加 status 后 firing/resolved 各占一行，dedup 仍由 dedup_key + 分诊层保证。
		index.Fields("source", "source_event_id", "status").Unique(),
		index.Fields("severity", "received_at"),
		index.Fields("is_noise"),
	}
}
