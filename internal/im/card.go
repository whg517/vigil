package im

import (
	"fmt"
	"strings"

	"github.com/kevin/vigil/ent"
)

// Card 平台无关的交互卡片数据结构。
// 各平台适配器把它转成各自的卡片 JSON（飞书 card schema / 钉钉 markdown 等）。
// 对应 capabilities/05-im-chatops.md §3。
type Card struct {
	// IncidentID 关联事件 ID，会注入到每个按钮的回调 value，
	// 使按钮点击回调能定位到具体事件。由 BuildCard 从 Incident 填入。
	IncidentID string
	// Header 标题区，如 "[CRITICAL] INC-0042 支付服务 5xx"
	Header string
	// Severity 严重度，用于图标/配色（critical/warning/info）
	Severity string
	// StatusBadge 状态标识文案，如 "已确认 by 张三"（已 ack 后更新）
	StatusBadge string
	// Rows 卡片正文键值行（服务/环境/时间/值班…）
	Rows []CardRow
	// Buttons 操作按钮（按权限已裁剪：无权按钮不进此列表）
	Buttons []CardButton
}

// CardRow 卡片正文一行。
type CardRow struct {
	Label string
	Value string
}

// CardButton 卡片操作按钮。
// Value 为点击后回传的 action（ack/escalate/resolve/detail），供回调解析。
type CardButton struct {
	Label string // 按钮文案，如 "✓ 确认"
	Value string // action 标识，如 "ack"
	Type  string // primary | default（视觉权重）
}

// 按钮/命令动作常量（与 incident.Service 动作字符串对齐，供回调解析与权限映射）。
const (
	ActionAck          = "ack"
	ActionEscalate     = "escalate"
	ActionResolve      = "resolve"
	ActionAddResponder = "add_responder"
	ActionDetail       = "detail" // 跳转 Web 详情页，非状态变更
)

// PermissionMap 把按钮 action 映射到所需权限点（字符串形式，与 auth.Permission 对齐）。
// 供 Renderer 调用 Authorizer.CheckAny 后按权限裁剪按钮。
// 对应 capabilities §3.1：无权按钮不显示。
var PermissionMap = map[string]string{
	ActionAck:          "incident.ack",
	ActionEscalate:     "incident.escalate",
	ActionResolve:      "incident.resolve",
	ActionAddResponder: "incident.add_responder",
	ActionDetail:       "incident.view",
}

// Renderer 按权限渲染卡片按钮。
// 它不直接依赖 auth 包（避免 im→auth 反向耦合），而是接受一个权限判定回调，
// 由 main 装配时注入 auth.Authorizer 的实现。这是 IM 不成权限后门的关键。
type Renderer struct {
	// HasPermission 判定 userID 在 teamScope 下是否拥有 perm（字符串形式权限点）。
	// 返回 map[perm]bool，缺失的 perm 视为 false。
	HasPermission func(userID int, teamScope *int, perms []string) (map[string]bool, error)
}

// NewRenderer 创建渲染器。
func NewRenderer(hasPermission func(int, *int, []string) (map[string]bool, error)) *Renderer {
	return &Renderer{HasPermission: hasPermission}
}

// BuildCard 用 Incident 构建卡片骨架（不含按钮），按钮由 WithPermittedButtons 注入。
func BuildCard(inc *ent.Incident, assignee string) *Card {
	sev := strings.ToUpper(string(inc.Severity))
	header := fmt.Sprintf("[%s] %s %s", sev, inc.Number, inc.Title)
	card := &Card{
		IncidentID: fmt.Sprintf("%d", inc.ID),
		Header:     header,
		Severity:   string(inc.Severity),
		Rows: []CardRow{
			{Label: "状态", Value: statusLabel(string(inc.Status))},
		},
	}
	if inc.Summary != "" {
		card.Rows = append(card.Rows, CardRow{Label: "摘要", Value: inc.Summary})
	}
	if inc.CurrentLevel > 0 {
		card.Rows = append(card.Rows, CardRow{Label: "当前层级", Value: fmt.Sprintf("Level %d", inc.CurrentLevel)})
	}
	if assignee != "" {
		card.Rows = append(card.Rows, CardRow{Label: "负责人", Value: assignee})
	}
	return card
}

// WithPermittedButtons 按 userID 的权限裁剪并附加按钮到卡片。
// allButtons 为候选按钮（含详情这种 view 权限）；无权的不进最终列表。
func (r *Renderer) WithPermittedButtons(card *Card, userID int, teamScope *int, allButtons []CardButton) error {
	if r.HasPermission == nil {
		// 无鉴权回调（如系统自发通知）：保守起见不渲染任何操作按钮。
		return nil
	}
	// 收集需要判定的权限点
	permSet := make(map[string]bool)
	perms := make([]string, 0, len(allButtons))
	for _, b := range allButtons {
		if p, ok := PermissionMap[b.Value]; ok && !permSet[p] {
			permSet[p] = true
			perms = append(perms, p)
		}
	}
	granted, err := r.HasPermission(userID, teamScope, perms)
	if err != nil {
		return err
	}
	for _, b := range allButtons {
		p := PermissionMap[b.Value]
		if granted[p] {
			card.Buttons = append(card.Buttons, b)
		}
	}
	return nil
}

// DefaultButtons 返回标准候选按钮集（ack/escalate/resolve/detail）。
// 实际渲染哪些由 WithPermittedButtons 按权限裁剪。
func DefaultButtons() []CardButton {
	return []CardButton{
		{Label: "✓ 确认", Value: ActionAck, Type: "primary"},
		{Label: "⬆ 升级", Value: ActionEscalate, Type: "default"},
		{Label: "✓ 解决", Value: ActionResolve, Type: "default"},
		{Label: "📋 详情", Value: ActionDetail, Type: "default"},
	}
}

// statusLabel 把 incident status 枚举转中文标签。
func statusLabel(status string) string {
	switch status {
	case "triggered":
		return "待响应"
	case "escalated":
		return "已升级"
	case "acked":
		return "已确认"
	case "resolved":
		return "已解决"
	case "closed":
		return "已关闭"
	default:
		return status
	}
}
