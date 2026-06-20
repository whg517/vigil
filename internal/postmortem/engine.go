// Package postmortem 实现能力域 12：复盘。
//
// 对应 docs/capabilities/08-postmortem.md：
// · 自动生成草稿（基于时间线 + AI 起草，AI 可降级）
// · 结构化模板（summary/impact/timeline/root_cause/action_items）
// · 改进项跟踪（action_items 有 owner/due/status，可对接工单）
// · 状态机：draft → in_review → published → archived
//
// 设计基线第 7 条：AI 横向 Copilot + human-in-the-loop，AI 起草人校对。
package postmortem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/timelineitem"
)

// LLMProvider LLM 接口（AI 起草用，可插拔）。nil 时降级为纯时间线草稿。
// 对应 capabilities/07 §B5。
type LLMProvider interface {
	// DraftSection 起草某章节。section 为章节名，context 含时间线/事件信息。
	// 返回草稿文本与 nil error；不可用时返回空串与 error（调用方降级）。
	DraftSection(ctx context.Context, section string, context map[string]any) (string, error)
}

// Engine 复盘引擎。
type Engine struct {
	db  *ent.Client
	llm LLMProvider // 可为 nil（无 AI 时降级）
}

// NewEngine 创建复盘引擎。llm 可为 nil。
func NewEngine(db *ent.Client, llm LLMProvider) *Engine {
	return &Engine{db: db, llm: llm}
}

// GenerateDraft 为某 Incident 生成复盘草稿。
// 流程：取事件 + 时间线 → 填 timeline 章节 → AI/规则填其他章节 → 落 Postmortem。
func (e *Engine) GenerateDraft(ctx context.Context, incID int) (*ent.Postmortem, error) {
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, fmt.Errorf("get incident %d: %w", incID, err)
	}

	// 取时间线（按时间正序）
	items, err := e.db.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
		Order(ent.Asc(timelineitem.FieldTimestamp)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query timeline: %w", err)
	}

	sections := map[string]any{}

	// timeline 章节始终从时间线自动填充（事实依据）
	sections["timeline"] = buildTimelineSection(items)

	// 其他章节：优先 AI 起草，降级为规则/占位
	ctxMap := map[string]any{
		"incident": inc,
		"timeline": items,
		"severity": string(inc.Severity),
		"summary":  inc.Summary,
		"title":    inc.Title,
	}

	sections["summary"] = e.draftOrFallback(ctx, "summary", ctxMap, fallbackSummary(inc))
	sections["impact"] = e.draftOrFallback(ctx, "impact", ctxMap, fallbackImpact(inc))
	sections["root_cause"] = e.draftOrFallback(ctx, "root_cause", ctxMap, "（待人工填写）")
	sections["what_went_well"] = []string{"（待人工补充）"}
	sections["what_went_wrong"] = []string{"（待人工补充）"}
	sections["action_items"] = []any{}

	// 生成方式：有 AI 贡献则 mixed，纯规则则 human
	genBy := postmortem.GeneratedByHuman
	if e.llm != nil {
		genBy = postmortem.GeneratedByMixed
	}

	// 检查是否已有复盘（避免重复）
	existing, err := e.db.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(incID))).
		Only(ctx)
	if err == nil && existing != nil {
		// 已有，更新草稿
		updated, err := e.db.Postmortem.UpdateOneID(existing.ID).
			SetSections(sections).
			SetGeneratedBy(genBy).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("update postmortem: %w", err)
		}
		return updated, nil
	}

	// 新建
	pm, err := e.db.Postmortem.Create().
		SetIncidentID(incID).
		SetStatus(postmortem.StatusDraft).
		SetGeneratedBy(genBy).
		SetSections(sections).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create postmortem: %w", err)
	}
	return pm, nil
}

// draftOrDraft 用 AI 起草，失败/无 LLM 则用 fallback。
func (e *Engine) draftOrFallback(ctx context.Context, section string, ctxMap map[string]any, fallback string) string {
	if e.llm == nil {
		return fallback
	}
	draft, err := e.llm.DraftSection(ctx, section, ctxMap)
	if err != nil || strings.TrimSpace(draft) == "" {
		return fallback
	}
	return draft
}

// buildTimelineSection 把时间线条目组装成 timeline 章节内容。
func buildTimelineSection(items []*ent.TimelineItem) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]string{
			"time":    it.Timestamp.Format(time.RFC3339),
			"type":    string(it.Type),
			"content": it.Content,
		})
	}
	return out
}

// fallbackSummary 无 AI 时的摘要草稿。
func fallbackSummary(inc *ent.Incident) string {
	return fmt.Sprintf("事件 %s（%s）：%s。", inc.Number, string(inc.Severity), inc.Title)
}

// fallbackImpact 无 AI 时的影响草稿（从事件元数据估算）。
func fallbackImpact(inc *ent.Incident) string {
	duration := "未知"
	if inc.ResolvedAt != nil {
		d := inc.ResolvedAt.Sub(inc.CreatedAt).Round(time.Minute)
		duration = d.String()
	}
	return fmt.Sprintf("持续时间约 %s（待补充影响用户数/损失）。", duration)
}

// Transition 状态流转（draft → in_review → published → archived）。
// 对应 capabilities §5 状态机。
func (e *Engine) Transition(ctx context.Context, pmID int, target postmortem.Status) (*ent.Postmortem, error) {
	pm, err := e.db.Postmortem.Get(ctx, pmID)
	if err != nil {
		return nil, fmt.Errorf("get postmortem: %w", err)
	}
	// 校验合法流转
	if !isValidTransition(postmortem.Status(pm.Status), target) {
		return nil, fmt.Errorf("invalid transition %s → %s", pm.Status, target)
	}
	update := e.db.Postmortem.UpdateOneID(pmID).SetStatus(target)
	if target == postmortem.StatusPublished && pm.PublishedAt == nil {
		now := time.Now()
		update.SetPublishedAt(now)
	}
	return update.Save(ctx)
}

// isValidTransition 校验状态机合法流转。
func isValidTransition(from, to postmortem.Status) bool {
	allowed := map[postmortem.Status][]postmortem.Status{
		postmortem.StatusDraft:     {postmortem.StatusInReview},
		postmortem.StatusInReview:  {postmortem.StatusPublished, postmortem.StatusDraft},
		postmortem.StatusPublished: {postmortem.StatusArchived},
		postmortem.StatusArchived:  {},
	}
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}
