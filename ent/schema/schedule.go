package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Schedule 排班 —— "此刻谁在班"的算法蓝图。
// 对应 data-model.md §3.2 Schedule。
// 注意：Schedule 是纯蓝图，不存当前值班人，由引擎实时计算。
type Schedule struct {
	ent.Schema
}

func (Schedule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Enum("type").Values("calendar", "rotation", "follow_the_sun"),
		field.String("timezone").Default("Asia/Shanghai"),
		// layers 存排班分层（primary/secondary/override），JSON 结构
		field.JSON("layers", []ScheduleLayer{}).Optional().Comment("排班分层"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// ScheduleLayer 排班分层（Schedule.layers 元素）。
// primary 没接到 → secondary；override 层覆盖临时换班。
type ScheduleLayer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`     // 如 "一线"
	Priority   int    `json:"priority"` // 数字越小优先级越高
	RotationID string `json:"rotation_id"`
}

func (Schedule) Edges() []ent.Edge {
	return []ent.Edge{
		// Schedule <- Team（归属团队）
		edge.From("team", Team.Type).Ref("schedules").Unique(),
		// Schedule <- Service（绑定到此排班的服务）
		edge.From("services", Service.Type).Ref("schedules"),
		// Schedule -> Rotation（排班轮换规则）
		edge.To("rotations", Rotation.Type),
		// Schedule -> Override（临时换班覆盖层，C5/M5.3）
		edge.To("overrides", Override.Type),
		// Schedule <- EscalationPolicy（作为升级目标）
		edge.From("escalation_policies", EscalationPolicy.Type).Ref("schedules"),
	}
}

// Override 临时换班 —— 在某时段内以顶替人覆盖 Rotation 计算结果（能力域 5 M5.3）。
// 对应 docs/capabilities/03-scheduling-escalation.md §2.4 Override。
// Override 是最高优先级层：时段命中时完全覆盖该 Schedule 的 Rotation 在班人。
// 支持自我换班（oncall 换己班，schedule.override 权限）与 admin 指派换班（换他人）。
type Override struct {
	ent.Schema
}

func (Override) Fields() []ent.Field {
	return []ent.Field{
		// start/end 覆盖时段（含 start，不含 end）；时段命中即由顶替人在班。
		field.Time("start_time").Comment("覆盖起始（含）"),
		field.Time("end_time").Comment("覆盖结束（不含）"),
		field.String("reason").Optional().Comment("换班原因，如 user_a 请假"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Override) Edges() []ent.Edge {
	return []ent.Edge{
		// Override <- Schedule（所属排班，Schedule.overrides 的反向）
		edge.From("schedule", Schedule.Type).Ref("overrides").Unique().Required(),
		// Override -> User（顶替人：时段内实际在班者）
		edge.To("user", User.Type).Unique().Required(),
		// Override -> User（创建人：自我换班时=顶替对象，admin 指派时=管理员）
		edge.To("created_by", User.Type).Unique(),
	}
}

// Rotation 轮班规则 —— Schedule 的子结构。
// 对应 data-model.md §3.2 Rotation。
// 班次序号 = floor((T - start_date) / shift_length)，当前值班 = participants[序号 mod 人数]。
type Rotation struct {
	ent.Schema
}

func (Rotation) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// shift_length 班次时长，如 "24h" / "1week"
		field.String("shift_length").Default("24h"),
		// handoff_time 交接时刻，如 "09:00"
		field.String("handoff_time").Default("09:00"),
		field.Enum("rotation_type").Values("daily", "weekly", "custom"),
		field.Time("start_date"),
		field.Time("end_date").Optional().Comment("null = 无限期"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Rotation) Edges() []ent.Edge {
	return []ent.Edge{
		// Rotation <- Schedule（所属排班）
		edge.From("schedule", Schedule.Type).Ref("rotations").Unique(),
		// Rotation <-> User（参与轮班的人，多对多）
		edge.To("participants", User.Type),
	}
}
