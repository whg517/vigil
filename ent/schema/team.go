package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Team 团队 —— 数据归属边界（软隔离）。
// 对应 data-model.md §3.1 Team。
// 团队可嵌套（parent_team_id），但权限不沿树继承。
type Team struct {
	ent.Schema
}

func (Team) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("slug").Unique().Comment("URL/标识用"),
		field.Text("description").Optional(),
		// parent_team_id 自引用，仅组织展示，权限不继承
		field.String("parent_team_id").Optional().Comment("父团队，仅组织展示，权限不继承"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Team) Edges() []ent.Edge {
	return []ent.Edge{
		// Team <-> User 多对多（团队拥有成员）
		edge.To("users", User.Type),
		// Team -> Service（团队拥有的服务）
		edge.To("services", Service.Type),
		// Team -> Schedule（团队拥有的排班）
		edge.To("schedules", Schedule.Type),
		// Team -> EscalationPolicy（团队拥有的升级策略）
		edge.To("escalation_policies", EscalationPolicy.Type),
		// Team -> EscalationPolicy（团队默认升级策略，方案C §3.5）。
		// 自动供给的 Service 继承此策略——无它则不自动供给，避免创建无策略的静默服务。
		// 应指向本团队 escalation_policies 之一（业务层校验）；Unique = 至多一个默认。
		edge.To("default_escalation_policy", EscalationPolicy.Type).Unique(),
		// Team -> Runbook（团队拥有的处置手册）
		edge.To("runbooks", Runbook.Type),
		// Team -> NotificationRule（团队拥有的通知规则）
		edge.To("notification_rules", NotificationRule.Type),
		// Team -> NotificationTemplate（团队拥有的通知模板）
		edge.To("notification_templates", NotificationTemplate.Type),
		// Team -> SuppressionRule（团队拥有的抑制规则）
		edge.To("suppression_rules", SuppressionRule.Type),
		// Team -> RoleBinding（团队作用域的角色绑定）
		edge.To("role_bindings", RoleBinding.Type),
		// Team -> Incident（归属事件）
		edge.To("incidents", Incident.Type),
		// Team -> Integration（归属接入点）
		edge.To("integrations", Integration.Type),
		// Team -> TicketIntegration（归属出向工单集成，T4.3）
		edge.To("ticket_integrations", TicketIntegration.Type),
		// Team -> Credential（归属加密托管凭据，T6.3）
		edge.To("credentials", Credential.Type),
		// Team -> WebhookSubscription（归属出站 webhook 动态订阅，N2.2）
		edge.To("webhook_subscriptions", WebhookSubscription.Type),
		// Team -> Subscription（订阅该团队 Incident 生命周期的定向订阅，T4.4）
		edge.To("subscriptions", Subscription.Type),
		// Team -> MetricsSnapshot（该团队的报表指标快照，T6.1 定时聚合）
		edge.To("metrics_snapshots", MetricsSnapshot.Type),
	}
}

func (Team) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("parent_team_id"),
	}
}

// 注：Team-User 为直接多对多关系（Member 角色/权限通过 RoleBinding 表达），
// 不再使用 Member through 实体。成员的"加入时间"等元数据按需后续以独立方式记录。
