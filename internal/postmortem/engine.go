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
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/timelineitem"

	"github.com/pgvector/pgvector-go"
)

// LLMProvider LLM 接口（AI 起草用，可插拔）。nil 时降级为纯时间线草稿。
// 对应 capabilities/07 §B5。
type LLMProvider interface {
	// DraftSection 起草某章节。section 为章节名，context 含时间线/事件信息。
	// 返回草稿文本与 nil error；不可用时返回空串与 error（调用方降级）。
	DraftSection(ctx context.Context, section string, context map[string]any) (string, error)
}

// Embedder 向量化接口（知识沉淀 M12.6 用）。nil 时 published 复盘不入库检索。
// 由 ai.Provider（GLMProvider）实现，注入后 published 时计算 embedding。
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Engine 复盘引擎。
type Engine struct {
	db       *ent.Client
	llm      LLMProvider // 可为 nil（无 AI 时降级）
	embedder Embedder    // 可为 nil（无 embedding 时 published 不入库检索）
}

// NewEngine 创建复盘引擎。llm 可为 nil。
func NewEngine(db *ent.Client, llm LLMProvider) *Engine {
	return &Engine{db: db, llm: llm}
}

// SetEmbedder 注入向量化器（main 装配时调用）。
// 配置后 published 复盘计算 embedding 入库，供知识沉淀检索（M12.6）。
func (e *Engine) SetEmbedder(em Embedder) { e.embedder = em }

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
	pm, err = update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update postmortem: %w", err)
	}
	// 知识沉淀（M12.6）：published 时计算 embedding 入库，供相似检索反哺。
	// embedder 未配置或计算失败不阻塞 publish（降级：复盘仍发布，但不进检索库）。
	if target == postmortem.StatusPublished && e.embedder != nil {
		_ = e.ensurePublishedEmbedding(ctx, pm)
	}
	return pm, nil
}

// ensurePublishedEmbedding 计算复盘内容的 embedding 并回写。
// 文本取 sections 的 summary + root_cause（最具语义代表性）。
// 失败仅返回 error，调用方 best-effort 忽略（不阻塞 publish）。
func (e *Engine) ensurePublishedEmbedding(ctx context.Context, pm *ent.Postmortem) error {
	text := extractPostmortemText(pm)
	if text == "" {
		return nil
	}
	vec, err := e.embedder.Embed(ctx, text)
	if err != nil || len(vec) == 0 {
		return fmt.Errorf("embed postmortem: %w", err)
	}
	nv := &schema.NullableVector{Valid: true}
	nv.Vector = pgvector.NewVector(vec)
	return e.db.Postmortem.UpdateOneID(pm.ID).SetEmbedding(nv).Exec(ctx)
}

// extractPostmortemText 从 sections 提取语义文本（summary + root_cause）。
func extractPostmortemText(pm *ent.Postmortem) string {
	var parts []string
	if s, ok := pm.Sections["summary"].(string); ok && s != "" {
		parts = append(parts, s)
	}
	if rc, ok := pm.Sections["root_cause"].(string); ok && rc != "" {
		parts = append(parts, rc)
	}
	return strings.Join(parts, " ")
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
