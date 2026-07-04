package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Notification 通知送达记录 —— 每次向某人某通道发送通知落一条（M13 / B22）。
// 对应 capabilities/04-notification.md §7.1 送达状态。
//
// 与 TimelineItem 的区别：TimelineItem 是事件全程可读留痕（面向复盘/协同），
// Notification 是「谁、走哪个通道、结果如何、为什么」的结构化送达账本，供：
//   - 送达率/失败原因 metrics 有数据源（可观测）
//   - 被静默/全败的通知可查、可补发（B22：不再直接丢弃无痕）
//   - GET /incidents/:id/notifications 查询端点
//
// 只追加、不修改（与审计一致）：一次发送 = 一条记录，状态在创建时定型。
type Notification struct {
	ent.Schema
}

func (Notification) Fields() []ent.Field {
	return []ent.Field{
		// channel 送达通道：im | phone | sms | email | webhook
		field.String("channel").NotEmpty().Comment("送达通道：im|phone|sms|email|webhook"),
		// target 送达目标标识（user id / email / 电话 / URL），便于排查「发给了谁」
		field.String("target").Optional().Comment("送达目标标识：user id/email/phone/url"),
		// user_id 关联用户（如能解析），冗余存便于按人查询；0=未关联具体人（如兜底群/webhook）
		field.Int("user_id").Default(0).Comment("关联用户 ID，0=无（群/webhook 等）"),
		// status 送达状态机（无中间态，创建时定型）：
		//   pending    已入队/在途（重试中）
		//   sent       已送达（通道返回成功）
		//   failed     发送失败（含全通道失败兜底后仍失败）
		//   suppressed 被静默时段拦截（未发，可补发）—— B22 不再直接丢弃
		field.Enum("status").
			Values("pending", "sent", "failed", "suppressed").
			Default("pending").
			Comment("送达状态：pending|sent|failed|suppressed"),
		// reason 状态原因：失败错误 / 静默原因（如 quiet_hours）/ 兜底说明
		field.String("reason").Optional().Comment("状态原因：失败错误/静默原因/兜底说明"),
		// level 触发时的升级层级（0=首轮）
		field.Int("level").Default(0).Comment("升级层级，0=首轮"),
		// severity 触发时的严重度快照（便于按严重度统计夜间打扰等）
		field.String("severity").Optional().Comment("严重度快照"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Notification) Edges() []ent.Edge {
	return []ent.Edge{
		// Notification <- Incident（关联事件；未路由兜底通知无单，可为空）
		edge.From("incident", Incident.Type).Ref("notifications").Unique(),
	}
}

func (Notification) Indexes() []ent.Index {
	return []ent.Index{
		// incident_id 是 edge 外键：GET /incidents/:id/notifications 查询高频
		index.Edges("incident"),
		index.Fields("status"),
		index.Fields("channel"),
		index.Fields("created_at"),
	}
}
