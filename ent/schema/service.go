package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Service 服务 —— 路由的锚点，软隔离的核心载体。
// 对应 data-model.md §3.2 Service。
// 告警的 label 匹配 Service 的 label → 命中路由。
type Service struct {
	ent.Schema
}

func (Service) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("slug").Unique(),
		field.Text("description").Optional(),
		// labels 用 JSON 存路由匹配标签（env/tier/service 等）
		field.JSON("labels", map[string]string{}).Optional().Comment("路由匹配标签"),
		field.Bool("auto_create_incident").Default(true).Comment("告警进来是否自动成 Incident"),
		field.Enum("status").Values("active", "disabled").Default("active"),
		// source 区分手工创建 vs 分诊自动供给（方案C，见 02-triage-routing §3.5）。
		// auto 服务由未路由告警即时创建，前端可据此筛选/批量转正/过期清理，治理防泛滥。
		field.Enum("source").Values("manual", "auto").Default("manual").Comment("来源：manual 手工 / auto 自动供给"),
		// provisioned_at 自动供给时间（source=auto 时填），供过期清理判定。
		field.Time("provisioned_at").Optional().Nillable().Comment("自动供给时间（source=auto）"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Service) Edges() []ent.Edge {
	return []ent.Edge{
		// Service <- Team（归属团队）
		edge.From("team", Team.Type).Ref("services").Unique(),
		// Service -> Integration（多个接入点汇入）
		edge.To("integrations", Integration.Type),
		// Service -> EscalationPolicy（绑定的升级策略）
		edge.To("escalation_policy", EscalationPolicy.Type).Unique(),
		// Service -> Schedule（可换班/复用排班）
		edge.To("schedules", Schedule.Type),
		// Service -> Runbook（关联处置手册）
		edge.To("runbooks", Runbook.Type),
		// Service -> Event（路由命中的事件）
		edge.To("events", Event.Type),
		// Service -> Incident（归属事件）
		edge.To("incidents", Incident.Type),
		// Service -> Subscription（订阅该服务 Incident 生命周期的定向订阅，T4.4）
		edge.To("subscriptions", Subscription.Type),
		// Service -> Service（服务依赖，自引用，T6.2/M4.4 服务拓扑）。
		// depends_on：本服务「依赖」的下游服务（如 web 依赖 db）。
		// dependents（反向边）：「依赖本服务」的上游服务，用于影响面分析基础
		// （本服务故障会影响哪些上游）。仅存关系，一层查询即可，不做完整拓扑算法。
		edge.To("depends_on", Service.Type).
			From("dependents").
			Comment("服务依赖：depends_on=依赖的下游，dependents=依赖本服务的上游（影响面）"),
	}
}

func (Service) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		// source 索引：治理时按来源筛选自动供给的服务（列表/批量转正/过期清理）。
		index.Fields("source"),
	}
}

// Integration 接入点 —— 告警的入口。
// 对应 data-model.md §3.2 Integration。
type Integration struct {
	ent.Schema
}

func (Integration) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// type 决定用哪个适配器做归一化
		field.Enum("type").Values(
			"webhook", "email", "prometheus", "zabbix", "grafana", "cloud", "api",
		),
		// config 存类型相关配置（URL/过滤/鉴权方式/限流等）
		field.JSON("config", map[string]any{}).Optional().Comment("类型相关配置"),
		// token 加密存储，webhook 鉴权用
		field.String("token").Sensitive().Comment("webhook 鉴权 token，加密存储"),
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Integration) Edges() []ent.Edge {
	return []ent.Edge{
		// Integration <- Team（归属团队）
		edge.From("team", Team.Type).Ref("integrations").Unique(),
		// Integration <- Service（默认归属服务）
		edge.From("service", Service.Type).Ref("integrations").Unique(),
		// Integration -> RawEvent（接收的原始告警）
		edge.To("raw_events", RawEvent.Type),
		// Integration -> Event（归一化产出）
		edge.To("events", Event.Type),
	}
}
