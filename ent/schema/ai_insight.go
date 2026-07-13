package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AIInsight AI 洞察 —— AI 横向 Copilot 的产物承载。
// 设计见 ADR-0022（AIInsight 横向 + HITL + 强制 evidence）。
// 所有 AI 产出带 evidence + 状态（human-in-the-loop）。
type AIInsight struct {
	ent.Schema
}

func (AIInsight) Fields() []ent.Field {
	return []ent.Field{
		// stage AI 介入的阶段
		field.Enum("stage").Values("triage", "diagnose", "postmortem", "copilot"),
		// type AI 产出类型
		// runbook_suggestion：处置 Copilot 推荐用哪个 Runbook（T3.3）。
		//   accept 只高亮/呈现该 Runbook，绝不触发执行——执行仍走 Runbook 两档安全
		//   （readonly 自动 / 写操作 require_approval），AI 推荐不绕过审批。
		field.Enum("type").Values(
			"dedup_suggestion", "severity_adjustment", "root_cause_hint",
			"similar_incident", "draft_summary", "postmortem_draft",
			"runbook_suggestion",
			// noise_suggestion：分诊阶段 AI 降噪建议（N1.4）——识别疑似噪声的 Event（labels/pattern），
			// accept 时沉淀为一条 SuppressionRule（source=ai）使「AI 学到的噪声」下次自动抑制。
			// 这是「AI 建议→规则沉淀」闭环，非机器学习回训；reject 仍只记录供分析（T3.4）。
			"noise_suggestion",
		),
		// content AI 产出（文本/结构化）
		field.JSON("content", map[string]any{}).Comment("AI 产出内容"),
		// confidence 置信度 0.0~1.0
		field.Float32("confidence").Default(0).Comment("置信度 0.0~1.0"),
		// evidence 依据（引用的 Event/日志/时间线）
		field.JSON("evidence", []map[string]any{}).Optional().Comment("依据，每条 AI 建议必须可溯源"),
		// status human-in-the-loop 状态
		field.Enum("status").Values(
			"suggested", "accepted", "rejected", "applied",
		).Default("suggested"),
		// resolved_by 改判人（accept/reject 的 user_id，S11 留痕）。
		// 0/未设 表示尚未改判（status=suggested）。
		field.Int("resolved_by").Optional().Comment("采纳/拒绝该建议的 user_id（S11 留痕）"),
		// resolved_at 改判时刻（S11 留痕）。
		field.Time("resolved_at").Optional().Nillable().Comment("采纳/拒绝该建议的时刻（S11 留痕）"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (AIInsight) Edges() []ent.Edge {
	return []ent.Edge{
		// AIInsight <- Incident
		edge.From("incident", Incident.Type).Ref("ai_insights").Unique(),
	}
}

func (AIInsight) Indexes() []ent.Index {
	return []ent.Index{
		// incident_id 是 edge 外键，用 index.Edges
		index.Edges("incident"),
		index.Fields("stage"),
		index.Fields("status"),
	}
}
