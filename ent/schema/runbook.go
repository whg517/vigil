package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Runbook 处置手册 —— "该干什么"。
// 对应 data-model.md §3.2 Runbook。
// 分两档：document（纯 Markdown）/ executable（可执行步骤链）。
type Runbook struct {
	ent.Schema
}

func (Runbook) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		// type 区分文档式 / 可执行式
		field.Enum("type").Values("document", "executable"),
		// trigger 触发条件
		field.JSON("trigger", map[string]any{}).Optional().Comment("触发：manual/on_incident/on_severity/on_label_match"),
		// auto_run 显式授权「触发命中即自动执行」——★ 仅对全只读诊断 Runbook 生效（IsReadOnly==true）。
		// 含任一写步骤的 Runbook 即使配 auto_run=true 也绝不自动执行（引擎侧硬守卫），只自动展示。
		// 默认 false：触发命中时只「展示」关联 Runbook 给响应者，不自动执行。
		field.Bool("auto_run").Default(false).Comment("触发命中即自动执行（仅全只读诊断 Runbook 生效，含写步骤绝不自动执行）"),
		// document 类型用 content_markdown
		field.Text("content_markdown").Optional().Comment("文档式 runbook 的 Markdown 内容"),
		// executable 类型用 steps
		field.JSON("steps", []RunbookStep{}).Optional().Comment("可执行步骤链"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// RunbookStep 可执行步骤（Runbook.steps 元素）。
// 对应 data-model.md §3.2 Runbook 步骤 + 能力域 9。
// 诊断类 readonly=true 由 Vigil 内置执行；
// 处置类 require_approval=true 强制人确认或对接外部平台。
type RunbookStep struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Action          StepAction `json:"action"`
	OnFailure       string     `json:"on_failure"`       // continue | abort | escalate
	RequireApproval bool       `json:"require_approval"` // 写操作必须人确认（human-in-the-loop）
}

// StepAction 步骤动作。
type StepAction struct {
	Type   string         `json:"type"` // diagnose | execute | notify | wait | approve
	Target StepTarget     `json:"target"`
	Params map[string]any `json:"params,omitempty"`
}

// StepTarget 动作目标（execute 时指向外部执行器）。
type StepTarget struct {
	Kind     string `json:"kind"` // http | ansible | jenkins | internal
	Endpoint string `json:"endpoint"`
	Readonly bool   `json:"readonly"` // diagnose 类强制只读
}

func (Runbook) Edges() []ent.Edge {
	return []ent.Edge{
		// Runbook <- Team（归属团队）
		edge.From("team", Team.Type).Ref("runbooks").Unique(),
		// Runbook <- Service（关联此 runbook 的服务）
		edge.From("services", Service.Type).Ref("runbooks"),
	}
}
