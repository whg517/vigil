// postmortem_adapter.go 让 GLMProvider 适配 postmortem.LLMProvider 接口。
// 负责把"起草某章节"转成具体 prompt，调 Provider.Complete。
package ai

import (
	"context"
	"fmt"
	"strings"
)

// PostmortemDraftAdapter 复盘起草适配器，实现 postmortem.LLMProvider。
// 内部委托给 Provider（GLM/OpenAI/...）。
type PostmortemDraftAdapter struct {
	provider Provider
}

// NewPostmortemDraftAdapter 创建复盘起草适配器。provider 为 nil 时所有调用返回错误（调用方应降级）。
func NewPostmortemDraftAdapter(p Provider) *PostmortemDraftAdapter {
	return &PostmortemDraftAdapter{provider: p}
}

// Available 是否可用于起草（provider 存在且可用）。
func (a *PostmortemDraftAdapter) Available() bool {
	return a.provider != nil && a.provider.Available()
}

// DraftSection 实现 postmortem.LLMProvider。
// section: summary/impact/root_cause/...；context 含事件+时间线信息。
func (a *PostmortemDraftAdapter) DraftSection(ctx context.Context, section string, contextMap map[string]any) (string, error) {
	if !a.Available() {
		return "", fmt.Errorf("draft adapter unavailable")
	}
	prompt := buildDraftPrompt(section, contextMap)
	out, err := a.provider.Complete(ctx, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// buildDraftPrompt 构造起草某章节的 prompt。
// 把事件上下文 + 时间线喂给 LLM，要求生成该章节的中文草稿。
func buildDraftPrompt(section string, ctxMap map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是一个运维事件复盘助手。根据以下事件信息，用简洁专业的中文起草复盘报告的「")
	sb.WriteString(sectionName(section))
	sb.WriteString("」章节（200字以内，直接给内容，不要寒暄）。\n\n")

	sb.WriteString("事件信息：\n")
	if t, ok := ctxMap["title"].(string); ok && t != "" {
		sb.WriteString(fmt.Sprintf("- 标题：%s\n", t))
	}
	if s, ok := ctxMap["severity"].(string); ok && s != "" {
		sb.WriteString(fmt.Sprintf("- 严重度：%s\n", s))
	}
	if s, ok := ctxMap["summary"].(string); ok && s != "" {
		sb.WriteString(fmt.Sprintf("- 概要：%s\n", s))
	}

	// 时间线（如有）
	if tl, ok := ctxMap["timeline"]; ok {
		sb.WriteString("\n时间线（关键事件）：\n")
		sb.WriteString(formatTimeline(tl))
	}

	// 按章节给不同指引
	switch section {
	case "root_cause":
		sb.WriteString("\n请基于时间线推测可能的根因，标注不确定性（如\"可能\"\"疑似\"），不要武断下结论。\n")
	case "impact":
		sb.WriteString("\n请估算影响（持续时间/影响面），无法确定的标注\"待补充\"。\n")
	}
	return sb.String()
}

// sectionName 中文章节名。
func sectionName(s string) string {
	names := map[string]string{
		"summary":           "摘要",
		"impact":            "影响",
		"root_cause":        "根因分析",
		"what_went_well":    "做得好的",
		"what_went_wrong":   "做得差的",
	}
	if n, ok := names[s]; ok {
		return n
	}
	return s
}

// formatTimeline 把时间线（ent.TimelineItem 切片）格式化为文本。
// 用 fmt.Sprintf + 类型断言，避免直接依赖 ent（保持 ai 包轻量）。
func formatTimeline(tl any) string {
	items, ok := tl.([]any)
	if !ok {
		// 可能是 []*ent.TimelineItem，用反射/Sprintf 兜底
		return fmt.Sprintf("%v", tl)
	}
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(fmt.Sprintf("- %v\n", it))
	}
	return sb.String()
}
