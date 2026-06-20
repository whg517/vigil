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
		// Team -> Runbook（团队拥有的处置手册）
		edge.To("runbooks", Runbook.Type),
		// Team -> NotificationRule（团队拥有的通知规则）
		edge.To("notification_rules", NotificationRule.Type),
		// Team -> RoleBinding（团队作用域的角色绑定）
		edge.To("role_bindings", RoleBinding.Type),
		// Team -> Incident（归属事件）
		edge.To("incidents", Incident.Type),
		// Team -> Integration（归属接入点）
		edge.To("integrations", Integration.Type),
	}
}

func (Team) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("parent_team_id"),
	}
}

// 注：Team-User 为直接多对多关系（Member 角色/权限通过 RoleBinding 表达），
// 不再使用 Member through 实体。成员的"加入时间"等元数据按需后续以独立方式记录。
