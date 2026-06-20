package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Incident 处理单元 —— 人介入的对象，Vigil 的核心实体。
// 对应 data-model.md §3.3 Incident。
// 由 Event 聚合而来，有状态机：
// triggered → escalated → acked → resolved → closed
type Incident struct {
	ent.Schema
}

func (Incident) Fields() []ent.Field {
	return []ent.Field{
		// 人类可读编号，如 INC-0042
		field.String("number").Unique().Comment("人类可读编号，如 INC-0042"),
		field.String("title").NotEmpty(),
		field.Enum("severity").Values("critical", "warning", "info"),
		// status 状态机：triggered | escalated | acked | resolved | closed
		field.Enum("status").Values(
			"triggered", "escalated", "acked", "resolved", "closed",
		).Default("triggered"),
		// priority P1/P2/P3，可由 severity + service tier 派生
		field.Enum("priority").Values("p1", "p2", "p3", "p4").Default("p3"),
		field.String("summary").Optional().Comment("当前概要，可随处置更新"),
		field.Int("escalated_count").Default(0).Comment("已升级次数"),
		field.Int("current_level").Default(0).Comment("当前升级层级"),
		// merged_into 若被合并，指向主 Incident id
		field.String("merged_into").Optional().Comment("若被合并，指向主 Incident"),
		// trigger 触发方式
		field.Enum("trigger_type").Values("auto", "manual", "merged").Default("auto"),
		field.String("trigger_source_event_id").Optional(),
		// war_room 作战室信息
		field.JSON("war_room", map[string]any{}).Optional().Comment("作战室：im_platform/im_channel_id/created_at"),
		field.Time("resolved_at").Optional().Nillable(),
		field.Time("closed_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Incident) Edges() []ent.Edge {
	return []ent.Edge{
		// Incident <- Team（归属团队）
		edge.From("team", Team.Type).Ref("incidents").Unique(),
		// Incident <- Service（归属服务）
		edge.From("service", Service.Type).Ref("incidents").Unique(),
		// Incident <- EscalationPolicy（使用的升级策略）
		edge.From("escalation_policy", EscalationPolicy.Type).Ref("incidents").Unique(),
		// Incident <- Event（聚合进来的告警，多对一）
		edge.From("events", Event.Type).Ref("incident"),
		// Incident -> User（当前责任人 assignee）
		edge.To("assignee", User.Type).Unique(),
		// Incident <-> User（所有参与响应的人 responders，多对多）
		edge.To("responders", User.Type),
		// Incident -> TimelineItem（时间线）
		edge.To("timeline", TimelineItem.Type),
		// Incident -> IncidentAction（操作审计）
		edge.To("actions", IncidentAction.Type),
		// Incident -> Postmortem（复盘，一对一）
		edge.To("postmortem", Postmortem.Type).Unique(),
		// Incident -> AIInsight（AI 洞察）
		edge.To("ai_insights", AIInsight.Type),
	}
}

func (Incident) Indexes() []ent.Index {
	return []ent.Index{
		// 按状态/严重度/团队查询是高频
		index.Fields("status", "severity"),
		// team_id / service_id 是 edge 外键，用 index.Edges
		index.Edges("team"),
		index.Edges("service"),
		index.Fields("resolved_at"),
	}
}
