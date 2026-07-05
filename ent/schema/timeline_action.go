package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TimelineItem 时间线条目 —— 全程留痕。
// 对应 data-model.md §3.3 TimelineItem。
// 自动捕获事件全程动作，为协同和复盘打基础。只追加，不修改。
type TimelineItem struct {
	ent.Schema
}

func (TimelineItem) Fields() []ent.Field {
	return []ent.Field{
		field.Time("timestamp").Default(time.Now),
		// type 条目类型
		field.Enum("type").Values(
			"incident_created", "event_attached", "status_changed",
			"escalated", "ack", "resolved", "reopened",
			"responder_added", "note_added",
			"runbook_executed", "runbook_suggested", "ai_insight", "im_message",
			// merged 合并留痕（M3.5/M3.6）：主单记「合入了哪些单」，被合并单记「并入了哪个主单」。
			"merged",
		),
		// actor 谁干的
		field.JSON("actor", map[string]string{}).Comment("actor: kind(system/user/integration/ai) + id"),
		field.Text("content").Comment("人类可读描述"),
		// detail 结构化详情
		field.JSON("detail", map[string]any{}).Optional(),
		// source 来源
		field.Enum("source").Values("web", "im", "api", "system", "ai"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (TimelineItem) Edges() []ent.Edge {
	return []ent.Edge{
		// TimelineItem <- Incident
		edge.From("incident", Incident.Type).Ref("timeline").Unique(),
	}
}

func (TimelineItem) Indexes() []ent.Index {
	return []ent.Index{
		// incident_id 是 edge 外键，用 index.Edges 索引
		index.Edges("incident"),
		index.Fields("timestamp"),
		index.Fields("type"),
	}
}

// IncidentAction 处置动作 —— 操作审计。
// 对应 data-model.md §3.3 IncidentAction。
// 所有对 Incident 的操作都落成 Action（审计 + 撤销/重放基础）。
type IncidentAction struct {
	ent.Schema
}

func (IncidentAction) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("type").Values(
			"ack", "escalate", "resolve", "reopen", "close", "snooze",
			"reassign", "add_responder", "runbook", "custom",
			// merge 人工合并（M3.5/M3.6）：把一个或多个 incident 合并进目标主单。
			"merge",
		),
		// actor
		field.JSON("actor", map[string]string{}).Comment("actor: kind + id"),
		// payload 动作参数
		field.JSON("payload", map[string]any{}).Optional(),
		// via 来源，IM-first 关键：看多少动作在 IM 完成
		field.Enum("via").Values("web", "im", "api", "automation"),
		field.Enum("result").Values("success", "failed", "pending").Default("success"),
		field.Time("timestamp").Default(time.Now).Immutable(),
	}
}

func (IncidentAction) Edges() []ent.Edge {
	return []ent.Edge{
		// IncidentAction <- Incident
		edge.From("incident", Incident.Type).Ref("actions").Unique(),
	}
}

func (IncidentAction) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("incident"),
		index.Fields("timestamp"),
		index.Fields("via", "timestamp"),
	}
}
