// card.go 把平台无关 Card 转成钉钉 ActionCard 消息体。
//
// 钉钉机器人消息类型 sampleActionCard 的 msgParam 结构：
//
//	{
//	  "title": "卡片标题",
//	  "text": "markdown 正文（支持 #、**粗体**、- 列表等）",
//	  "btns": [ { "title": "确认", "actionURL": "https://..." } ],
//	  "singleTitle" / "singleURL": 单按钮模式（不用）
//	}
//
// Vigil 的"按钮回调"在钉钉里通过两种方式承载：
//  1. ActionCard 按钮（本实现）：actionURL 形如 vigil://action?act=ack&inc=42，
//     钉钉把按钮点击转成卡片事件回调推给 Vigil（content 字段含该 URL）。
//  2. 独立跳转按钮跳到 Web 详情页（detail 动作）。
//
// 与飞书的差异：飞书按钮带 value map 直接回传 action；钉钉按钮带 actionURL，
// 由 ParseCallback 从 actionURL 解析出 action + incident_id。
package dingtalk

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kevin/vigil/internal/im"
)

// actionCardParam 钉钉 sampleActionCard 消息体（msgParam 的 JSON 结构）。
type actionCardParam struct {
	Title string          `json:"title"`
	Text  string          `json:"text"`
	Btns  []actionCardBtn `json:"btns,omitempty"`
}

// actionCardBtn ActionCard 按钮。
type actionCardBtn struct {
	Title     string `json:"title"`
	ActionURL string `json:"actionURL"`
}

// CardToActionCard 把平台无关 Card 转成钉钉 sampleActionCard 的 msgParam（JSON 字符串）。
// 返回 msgParam（不含外层 msgKey）。
func CardToActionCard(card *im.Card) (string, error) {
	if card == nil {
		return "", fmt.Errorf("nil card")
	}
	p := actionCardParam{
		Title: card.Header,
		Text:  buildMarkdown(card),
	}
	// 按钮：detail 跳 Web 链接（/incidents/:id），其余动作用 vigil:// scheme 承载回调数据
	for _, b := range card.Buttons {
		btn := actionCardBtn{Title: b.Label}
		if b.Value == im.ActionDetail {
			btn.ActionURL = fmt.Sprintf("vigil:///incidents/%s", card.IncidentID)
		} else {
			btn.ActionURL = fmt.Sprintf("vigil://action?act=%s&inc=%s", b.Value, card.IncidentID)
		}
		p.Btns = append(p.Btns, btn)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("dingtalk marshal actioncard: %w", err)
	}
	return string(raw), nil
}

// buildMarkdown 构造卡片正文（markdown）。
// 标题已在 header 用，正文放状态徽章 + 键值行。
func buildMarkdown(card *im.Card) string {
	var sb strings.Builder
	if card.StatusBadge != "" {
		fmt.Fprintf(&sb, "**%s**\n\n", card.StatusBadge)
	}
	for _, r := range card.Rows {
		fmt.Fprintf(&sb, "- **%s**：%s\n", r.Label, r.Value)
	}
	if sb.Len() == 0 {
		return "（无详情）"
	}
	return sb.String()
}

// severityColor 钉钉 ActionCard 无 header 配色（飞书有 red/orange 模板），
// 这里把严重度编进标题前缀（emoji），视觉区分。
// 钉钉卡片本身不分色，故只返回 emoji 供标题使用。
func severityEmoji(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "🔴"
	case "warning":
		return "🟠"
	case "info":
		return "🔵"
	default:
		return "⚪"
	}
}

// TitleWithSeverity 给标题加严重度 emoji 前缀（钉钉 ActionCard 无 header 配色，用 emoji 区分）。
func TitleWithSeverity(card *im.Card) string {
	return severityEmoji(card.Severity) + " " + card.Header
}
