package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MetricsSnapshot 报表指标快照 —— 定时聚合的预计算结果（T6.1，能力域 15 §B3）。
//
// 对应 docs/capabilities/10-integrations-analytics.md §B3 数据来源：
// 「聚合任务：定时 Asynq 任务（每小时/每日）」。大数据量下实时聚合（全表扫 Event/Incident）
// 慢且抖动，故周期性把各维度指标预计算存库，报表端点可选读快照（source=snapshot）快速返回；
// 默认仍读实时（source=realtime）保准确，快照是「可选的加速旁路」而非唯一真相。
//
// 一条快照 = 某个团队（或 org 全局）在某个周期（period_start~period_end）内各维度指标的定格。
//
// 归属：team 边 nullable。
//   - 挂 team：该团队在该周期的指标（team 级 Leader 读快照时按可见 team 过滤，复用 scope）。
//   - 不挂 team（team 为 NULL）：org 全局聚合（org_admin 视图 / Dashboard 兜底）。
//
// 幂等：同 (team, period, period_start) 唯一——重跑聚合覆盖旧值（UpsertOne），不产重复行。
type MetricsSnapshot struct {
	ent.Schema
}

func (MetricsSnapshot) Fields() []ent.Field {
	return []ent.Field{
		// period 聚合粒度：hourly（每小时快照）/ daily（每日快照）。
		field.Enum("period").
			Values("hourly", "daily").
			Default("daily").
			Comment("聚合粒度：hourly 每小时 / daily 每日"),
		// period_start 快照覆盖窗口起点（含），period_end 终点（不含）。幂等键的一部分。
		field.Time("period_start").Comment("快照窗口起点（含）"),
		field.Time("period_end").Comment("快照窗口终点（不含）"),

		// —— 告警度量（AlertMetrics）——
		field.Int("alerts_total").Default(0).Comment("接入总量"),
		field.Int("alerts_notified").Default(0).Comment("触发通知的（非噪音）"),
		field.Int("alerts_unrouted").Default(0).Comment("未命中路由"),
		field.Float("noise_rate").Default(0).Comment("降噪率 0~1"),

		// —— 事件度量（IncidentMetrics）——
		field.Int("incidents_total").Default(0).Comment("Incident 总数"),
		field.Int("incidents_resolved").Default(0).Comment("已解决数"),
		field.Float("mtta_seconds").Default(0).Comment("平均确认时长（秒）"),
		field.Float("mttr_seconds").Default(0).Comment("平均解决时长（秒）"),
		// by_severity/by_status 按 severity/status 分布（JSON，避免为每档单开列）。
		field.JSON("by_severity", map[string]int{}).Optional().Comment("severity 分布"),
		field.JSON("by_status", map[string]int{}).Optional().Comment("status 分布"),

		// —— 复盘度量（PostmortemMetrics）——
		field.Int("postmortems_total").Default(0).Comment("复盘总数"),
		field.Int("postmortems_published").Default(0).Comment("已发布/归档数"),
		field.Float("completion_rate").Default(0).Comment("复盘完成率 0~1"),

		// created_at 快照生成时间（可观测：判断快照新鲜度）。
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (MetricsSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		// team nullable：挂 team 为团队级快照，NULL 为 org 全局快照。
		edge.From("team", Team.Type).Ref("metrics_snapshots").Unique(),
	}
}

func (MetricsSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		// 幂等 + 查询主键：同一 team+period+period_start 只保留一行（重跑覆盖）。
		// team 边列名 team_metrics_snapshots；NULL team（org 全局）在多数数据库唯一约束下
		// 允许多 NULL 行，故 org 全局的去重由聚合器写入前先删旧值兜底（见 aggregator）。
		index.Fields("period", "period_start").
			Edges("team").
			Unique(),
		// 按周期时间范围查快照（报表读快照路径）。
		index.Fields("period", "period_start"),
	}
}
