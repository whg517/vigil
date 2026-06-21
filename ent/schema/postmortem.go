package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Postmortem 复盘 —— 闭环学习。
// 对应 data-model.md §3.3 Postmortem。
// 状态机：draft → in_review → published → archived。
type Postmortem struct {
	ent.Schema
}

func (Postmortem) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").Values(
			"draft", "in_review", "published", "archived",
		).Default("draft"),
		// generated_by 草稿来源
		field.Enum("generated_by").Values("ai", "human", "mixed").Default("mixed"),
		// sections 结构化内容（summary/impact/timeline/root_cause/...）
		field.JSON("sections", map[string]any{}).Comment("结构化内容章节"),
		field.Time("published_at").Optional().Nillable(),
		// embedding 语义向量（pgvector），published 后计算入库，用于知识沉淀检索（M12.6）。
		// 复用 Incident.embedding 的 NullableVector 模式（见 incident.go）。
		// 列类型 vector(1536)；仅 postgres 支持，sqlite 测试用 blob。
		field.Other("embedding", &NullableVector{}).
			SchemaType(map[string]string{
				"postgres": "vector(1536)",
				"sqlite3":  "blob",
			}).
			Optional().
			Comment("语义向量，published 复盘入库后计算，知识沉淀检索用"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Postmortem) Edges() []ent.Edge {
	return []ent.Edge{
		// Postmortem <- Incident（一对一）
		edge.From("incident", Incident.Type).Ref("postmortem").Unique(),
		// Postmortem -> ActionItem（改进项）
		edge.To("action_items", ActionItem.Type),
	}
}

// ActionItem 改进项 —— 复盘的落地行动。
// 对应 data-model.md §3.3 Postmortem.sections.action_items（提升为独立实体便于跟踪）。
type ActionItem struct {
	ent.Schema
}

func (ActionItem) Fields() []ent.Field {
	return []ent.Field{
		field.Text("description").NotEmpty(),
		// owner_id 责任人（存 id 字符串，避免与 User 强耦合）
		field.String("owner_id").Optional(),
		field.Time("due_date").Optional().Nillable(),
		field.Enum("status").Values("open", "in_progress", "done").Default("open"),
		// tracker_url 对接外部工单（Jira/禅道）
		field.String("tracker_url").Optional().Comment("对接外部工单"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ActionItem) Edges() []ent.Edge {
	return []ent.Edge{
		// ActionItem <- Postmortem
		edge.From("postmortem", Postmortem.Type).Ref("action_items").Unique(),
	}
}

func (ActionItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("due_date"),
	}
}
