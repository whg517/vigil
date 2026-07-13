package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Subscription 定向订阅 —— 让干系人（尤其 subscriber / 团队 Leader）订阅关注对象，
// 在其 Incident 生命周期变更时收到定向通知（T4.4，旅程 E.2）。
//
// 设计脉络：通知体系（ADR-0017）之上的定向订阅补充。
// 原先 subscriber 只能「进值班群围观」（当前没有真正订阅），
// 本实体补齐「按团队/服务订阅 → 状态变更定向告知」的机制。
//
// 粒度取舍：按 team 或 service 订阅其 Incident 生命周期（二选一，均 nullable）。
//   - team 订阅：该 team 归属的所有 Incident 生命周期事件都告知（团队 Leader 看全貌）。
//   - service 订阅：只关心某服务（更细粒度，业务方只盯自己的服务）。
//
// 不做 severity 级订阅（severity 由 min_severity 偏好过滤，非订阅维度）。
//
// 与升级 target 的区别：订阅是「多一类通知接收人来源」——升级 target 是处置责任人（值班/升级链），
// 订阅者是只读干系人（被告知但不处置）。定向通知复用 T2.2 分发链 + 送达记录，
// 但 quiet_hours 对非值班订阅者生效（夜间非 critical 抑制，避免打扰）。
type Subscription struct {
	ent.Schema
}

func (Subscription) Fields() []ent.Field {
	return []ent.Field{
		// channels 订阅者的通道偏好（有序降级链）。空=用全局默认链兜底。
		// 允许订阅者选「只走邮件」（低打扰，符合 subscriber 只读干系人定位）。
		field.JSON("channels", []string{}).Optional().Comment("通道偏好（有序降级链），空=默认链"),
		// min_severity 最低告知严重度：低于此阈值的 Incident 不定向告知（默认 warning，屏蔽 info 噪音）。
		// 阈值序：critical > warning > info。选 warning 表示「warning 及以上都告知，info 不告知」。
		field.Enum("min_severity").
			Values("critical", "warning", "info").
			Default("warning").
			Comment("最低告知严重度，低于此不定向通知"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Subscription) Edges() []ent.Edge {
	return []ent.Edge{
		// Subscription <- User（订阅拥有者，必填——订阅是「谁订阅了什么」）。
		edge.From("user", User.Type).Ref("subscriptions").Unique().Required(),
		// Subscription <- Team（订阅的团队 scope，与 service 二选一，nullable）。
		edge.From("team", Team.Type).Ref("subscriptions").Unique(),
		// Subscription <- Service（订阅的服务 scope，与 team 二选一，nullable）。
		edge.From("service", Service.Type).Ref("subscriptions").Unique(),
	}
}

func (Subscription) Indexes() []ent.Index {
	return []ent.Index{
		// 按 user 查「我的订阅」（GET /subscriptions）高频。
		index.Edges("user"),
		// 事件触发时按 team/service 反查订阅者（定向通知路由）高频。
		index.Edges("team"),
		index.Edges("service"),
	}
}
