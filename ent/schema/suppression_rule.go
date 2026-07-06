package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// SuppressionRule 抑制规则 —— "少打扰"核心（能力域 3 M3.2）。
// 对应 capabilities/02-triage-routing.md §2.3。
// 满足条件（label 匹配 + 时间窗 + 严重度过滤）时主动抑制：
//   - action=suppress：Event 标记 is_noise=true，不创建/并入 Incident，仅留痕（可申诉）
//   - action=reduce_severity：降低 Event 严重度（critical 不降，preserve_critical 守卫）
//
// preserve_critical 默认 true：critical 告警不被抑制，避免误杀真故障。
type SuppressionRule struct {
	ent.Schema
}

func (SuppressionRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// kind 规则类别，区分「日常降噪抑制」与「计划内维护窗口」，便于前端做专属入口/列表：
		//   - adhoc      ：日常降噪抑制（默认）。长期生效，靠 enabled 启停；无固定起止，
		//                  time_window 可空（永久）或用于「每天某时段」类粗粒度限制。
		//   - maintenance：计划内维护窗口。有明确起止时间窗（time_window.{start,end} RFC3339），
		//                  仅在窗内抑制、窗外正常告警；配 expires_at 后到期自动失效（软失效，无需人工清理）。
		// 语义边界：kind 只是分类标签，不改变匹配逻辑本身——两类都走同一套 matchRule
		//（label 全等 + time_window + severity_filter）。前端据此把维护窗口与日常规则分列展示。
		field.Enum("kind").Values("adhoc", "maintenance").Default("adhoc").
			Comment("规则类别：adhoc 日常降噪 / maintenance 计划内维护窗口（有起止时间窗、到期自动失效）"),
		// match_labels Event.labels 匹配条件（全部 key 精确匹配才算命中），JSON
		field.JSON("match_labels", map[string]string{}).Comment("label 匹配条件，全等匹配"),
		// time_window 时间窗口（维护窗口场景），JSON：{start,end} 或 {expires_at}
		// 为空表示无时间窗限制（永久生效，靠 enabled 控制启停）
		field.JSON("time_window", map[string]any{}).Optional().Comment("时间窗口，空=无限制"),
		// severity_filter 命中的严重度范围（空=所有严重度），如 ["info","warning"]
		field.JSON("severity_filter", []string{}).Optional().Comment("命中的严重度，空=所有"),
		// action 命中后动作：suppress（抑制）| reduce_severity（降级）
		field.Enum("action").Values("suppress", "reduce_severity").Default("suppress"),
		// reduce_to 降级目标严重度（action=reduce_severity 时用），如 "warning"
		field.String("reduce_to").Optional().Comment("降级目标严重度"),
		// preserve_critical critical 告警不被抑制（即使命中条件），避免误杀真故障
		field.Bool("preserve_critical").Default(true).Comment("critical 不被抑制"),
		field.Bool("enabled").Default(true),
		// source 规则来源（N1.4 AI 噪声学习闭环）：
		//   - manual：人工在通知配置里手建（默认，与既有行为一致）。
		//   - ai    ：由被采纳的 AI 降噪建议沉淀而来（accept noise_suggestion → 生成本规则）。
		// 语义边界：这是「AI 建议→规则沉淀」，非机器学习模型回训；规则一旦生成即普通抑制规则，
		// team_admin 可见、可撤（禁用/删除）。标 source=ai 只为可溯源、可审计、可与人工规则区分。
		field.Enum("source").Values("manual", "ai").Default("manual").Comment("规则来源：manual 人工 / ai 由采纳的降噪建议沉淀"),
		// source_insight_id 沉淀本规则的那条 AIInsight id（source=ai 时有值，N1.4）。
		// 幂等键：同一条降噪建议重复 accept 不重复建规则（据此查重）。0/未设=非 AI 来源。
		field.Int("source_insight_id").Optional().Comment("沉淀本规则的 AIInsight id（幂等键，source=ai 时有值）"),
		// expires_at 规则过期时间（自动失效，可选）
		field.Time("expires_at").Optional().Nillable().Comment("规则过期时间"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (SuppressionRule) Edges() []ent.Edge {
	return []ent.Edge{
		// SuppressionRule <- Team（归属团队）
		edge.From("team", Team.Type).Ref("suppression_rules").Unique(),
	}
}
