package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// NotificationTemplate 通知模板 —— 通知内容可配置（能力域 7 M7.5）。
// 对应 capabilities/04-notification.md §6。
// 用 Go template 语法渲染 title_template / body_template，
// 模板变量为 incident/service/targets/level/action_url/now。
// 内置默认模板（builtin=true）由代码常量提供，seed 时 upsert；用户模板按 name 覆盖。
type NotificationTemplate struct {
	ent.Schema
}

func (NotificationTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Comment("模板名，唯一标识，如 default_im_card"),
		// channel 适用通道：im | email | webhook
		field.Enum("channel").Values("im", "email", "webhook"),
		// format 渲染格式：text（纯文本）/ interactive_card（IM 卡片，带按钮）
		field.Enum("format").Values("text", "interactive_card"),
		// title_template 标题模板（Go template）
		field.String("title_template").Comment("标题模板，Go template 语法"),
		// body_template 正文模板（Go template）
		field.Text("body_template").Comment("正文模板，Go template 语法"),
		// actions 卡片按钮（format=interactive_card 时用），JSON 结构
		field.JSON("actions", []TemplateAction{}).Optional().Comment("卡片按钮定义"),
		// builtin 是否内置模板（内置模板 seed 时 upsert，不可被用户删除）
		field.Bool("builtin").Default(false).Comment("内置模板标记"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// TemplateAction 通知模板按钮（interactive_card 格式用）。
// type 为动作标识（ack/escalate/resolve/detail），渲染时映射到卡片按钮。
type TemplateAction struct {
	Type  string `json:"type"`  // ack | escalate | resolve | detail
	Label string `json:"label"` // 按钮文案，如 "确认"
}

func (NotificationTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		// NotificationTemplate <- Team（归属团队，nil=全局模板）
		edge.From("team", Team.Type).Ref("notification_templates").Unique(),
	}
}
