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
// preserve_critical 默认 true：critical 告警不被抑制，避免误杀真故障。
type SuppressionRule struct {
	ent.Schema
}

func (SuppressionRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
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
