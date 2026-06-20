package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// EscalationPolicy 升级策略 —— "没人理找下一个"。
// 对应 data-model.md §3.2 EscalationPolicy。
// 有序升级层级，每层有延迟、目标、通道。
type EscalationPolicy struct {
	ent.Schema
}

func (EscalationPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// repeat_times 当前 level 未 ack 时重复通知次数
		field.Int("repeat_times").Default(0),
		// levels 有序升级层级，JSON 结构
		field.JSON("levels", []EscalationLevel{}).Comment("有序升级层级"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// EscalationLevel 升级层级（EscalationPolicy.levels 元素）。
type EscalationLevel struct {
	Level         int      `json:"level"`           // 层级序号
	DelayMinutes  int      `json:"delay_minutes"`   // 进入此 level 后多久发通知
	Targets       []Target `json:"targets"`         // 通知目标
	NotifyChannel []string `json:"notify_channels"` // im | phone | sms | email
}

// Target 升级通知目标。
type Target struct {
	Type       string `json:"type"`        // schedule | user | team
	TargetID   string `json:"target_id"`   // schedule_id / user_id / team_id
}

func (EscalationPolicy) Edges() []ent.Edge {
	return []ent.Edge{
		// EscalationPolicy <- Team（归属团队）
		edge.From("team", Team.Type).Ref("escalation_policies").Unique(),
		// EscalationPolicy <- Service（绑定此策略的服务）
		edge.From("services", Service.Type).Ref("escalation_policy"),
		// EscalationPolicy -> Schedule（升级目标引用的排班）
		edge.To("schedules", Schedule.Type),
		// EscalationPolicy -> Incident（被 Incident 引用，Incident.escalation_policy 的反向）
		edge.To("incidents", Incident.Type),
	}
}

// NotificationRule 通知规则 —— 通知层的配置。
// 对应 data-model.md §3.2 NotificationRule。
// 与 EscalationPolicy 区别：升级策略管"找不到人怎么办"，
// 通知规则管"用哪种通道、什么模板、何时静默"。
type NotificationRule struct {
	ent.Schema
}

func (NotificationRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// condition 触发条件（severity/team/service），JSON
		field.JSON("condition", map[string]any{}).Comment("触发条件"),
		// channels 启用通道 im | phone | sms | email | webhook
		field.JSON("channels", []string{}).Comment("启用通道"),
		field.String("template_id").Optional().Comment("通知模板"),
		// quiet_hours 静默时段配置
		field.JSON("quiet_hours", map[string]any{}).Optional().Comment("静默时段"),
		field.Bool("enabled").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (NotificationRule) Edges() []ent.Edge {
	return []ent.Edge{
		// NotificationRule <- Team（归属团队）
		edge.From("team", Team.Type).Ref("notification_rules").Unique(),
	}
}
