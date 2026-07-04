// copilot.go 处置阶段 AI（Copilot）：在处置阶段产出「带 evidence 的建议」，落 AIInsight。
//
// 对应 docs/capabilities/07-timeline-ai.md §B2（处置阶段的 AI 介入）与 roadmap T3.3 / 审计 C15：
//   - runbook_suggestion：AI 基于 Incident 内容 + 相似历史事件（其 runbook_executed 记录）
//     推荐「这类故障通常用 Runbook X」，产出带 evidence 的推荐（引用相似事件 + 其历史执行的
//     Runbook）。★ 安全红线：accept 只高亮/呈现该 Runbook，绝不触发执行——执行仍走 Runbook
//     两档安全（readonly 自动 / 写操作 require_approval），AI 推荐不绕过审批（见 diagnose.go
//     applyInsight：runbook_suggestion 无实际应用动作，accept=终态，不碰 runbook 引擎）。
//   - draft_summary：AI 草拟 Incident 当前状态摘要（供快速了解 / 交接），产出 draft_summary
//     建议。与复盘 postmortem_draft 区分：draft_summary=处理中的实时状态摘要（stage=copilot），
//     postmortem_draft=复盘全文（stage=postmortem，不在本任务）。
//
// 全程遵循 §B1 设计原则与本任务基线（与 triage_ai.go 同款）：
//   - human-in-the-loop：产出 status=suggested，须人 accept/reject 才生效。
//   - evidence 强制：runbook 推荐无候选 Runbook / 无相似历史依据不产出；摘要以时间线为 evidence。
//   - 置信度门槛：runbook 推荐低于阈值（默认 0.6，Q2）不产出。
//   - 可降级：LLM 不可用 / 调用失败 → 不产出建议，处置主流程继续不阻断（复用诊断链降级语义）。
//   - 触发：手动端点（POST /incidents/:id/ai-copilot）或随诊断链，不阻塞。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/timeline"
)

// copilotSimilarLimit 处置推荐取相似历史事件的上限（其 runbook_executed 记录作为推荐依据）。
const copilotSimilarLimit = 5

// CopilotEngine 处置阶段 AI 引擎。产出 runbook_suggestion / draft_summary 建议，落 AIInsight。
// 与 TriageAIEngine 同款结构：持有 db + provider + finder + recorder，provider 不可用时全程降级。
type CopilotEngine struct {
	db       *ent.Client
	provider Provider // LLM 提供方，nil 或不可用时降级（不产出建议）
	// finder 相似检索器（runbook 推荐用）：找相似历史事件，看它们当时执行了哪些 Runbook。
	// nil 时 runbook 推荐降级为「只按当前 Service 关联的 Runbook 候选」（无历史 evidence 加权）。
	finder SimilarFinder
	// recorder 时间线记录器。产出 AIInsight 后写 ai_insight 时间线（全程留痕）。nil 时跳过。
	recorder *timeline.Recorder
	// confidenceThreshold runbook 推荐产出置信度门槛（默认 0.6）。
	confidenceThreshold float32
}

// NewCopilotEngine 创建处置 Copilot 引擎。置信度门槛用默认 0.6，可经 SetConfidenceThreshold 覆盖。
func NewCopilotEngine(db *ent.Client, p Provider) *CopilotEngine {
	return &CopilotEngine{db: db, provider: p, confidenceThreshold: defaultConfidenceThreshold}
}

// SetSimilarFinder 注入相似检索器（runbook 推荐用）。装配时传入 *DiagnoseEngine。
func (e *CopilotEngine) SetSimilarFinder(f SimilarFinder) { e.finder = f }

// SetRecorder 注入时间线记录器（装配时调用）：产出 AI 洞察后写 ai_insight 时间线。
func (e *CopilotEngine) SetRecorder(r *timeline.Recorder) { e.recorder = r }

// SetConfidenceThreshold 覆盖置信度门槛（<=0 时保留默认，避免误配为 0 使一切建议都产出）。
func (e *CopilotEngine) SetConfidenceThreshold(t float32) {
	if t > 0 {
		e.confidenceThreshold = t
	}
}

// available 是否可产出 AI 建议（LLM 配置且可用）。不可用时调用方降级不产出。
func (e *CopilotEngine) available() bool {
	return e.provider != nil && e.provider.Available()
}

// CopilotResult 处置 Copilot 一次运行的产出（runbook 推荐与摘要草拟均可能为 nil）。
type CopilotResult struct {
	Runbook *ent.AIInsight `json:"runbook,omitempty"` // runbook_suggestion 建议（未产出为 nil）
	Summary *ent.AIInsight `json:"summary,omitempty"` // draft_summary 建议（未产出为 nil）
}

// AnalyzeIncident 对一个 Incident 跑处置 Copilot 全流程：runbook 推荐 + 摘要草拟。
// 是手动端点（POST /incidents/:id/ai-copilot）与随诊断链触发共用的入口。
//
// 降级：LLM 不可用时直接返回空结果（两建议均 nil），不报错、不落 AIInsight——
// 保证处置主流程不被 AI 阻断。单类建议内部失败（LLM 报错 / 无 evidence / 低置信度）不影响另一类。
func (e *CopilotEngine) AnalyzeIncident(ctx context.Context, incID int) (*CopilotResult, error) {
	res := &CopilotResult{}
	if !e.available() {
		return res, nil // 降级：无 LLM，不产出任何建议
	}
	// incident 不存在归一为 error（手动端点据此返 404）；异步触发方忽略即可。
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, err
	}

	// runbook 推荐（单类失败不影响 summary）
	if rb, rerr := e.suggestRunbook(ctx, inc); rerr != nil {
		slog.Warn("copilot: runbook suggestion failed, skip", "incident_id", incID, "error", rerr)
	} else {
		res.Runbook = rb
	}

	// draft_summary 摘要草拟（单类失败不影响返回）
	if sm, serr := e.draftSummary(ctx, inc); serr != nil {
		slog.Warn("copilot: draft summary failed, skip", "incident_id", incID, "error", serr)
	} else {
		res.Summary = sm
	}

	return res, nil
}

// runbookCandidate 处置推荐的候选 Runbook（关联 Service 的 Runbook）+ 相似历史事件用过它的痕迹。
type runbookCandidate struct {
	rb *ent.Runbook
	// pastUses 相似历史事件里执行过该 Runbook 的痕迹（incident 编号列表），作为「这类故障用过它」的 evidence。
	pastUses []pastRunbookUse
}

// pastRunbookUse 一次相似历史事件里的 Runbook 执行痕迹。
type pastRunbookUse struct {
	incidentID     int
	incidentNumber string
	stepName       string // 执行的步骤名（来自 runbook_executed 时间线 detail.step）
}

// suggestRunbook 产出 runbook_suggestion 建议：AI 推荐这类故障通常用哪个 Runbook。
//
// 依据（evidence）= 当前 Service 关联的候选 Runbook + 相似历史事件里执行过的 Runbook 步骤痕迹。
// 无候选 Runbook（Service 未关联任何 Runbook）→ 不产出（无可推荐对象）。
// LLM 判断无合适 Runbook / 置信度不足 / 推荐的 id 不在候选集（防幻觉）时不产出。
//
// ★ 安全红线：产出的 content 只含 recommended_runbook_id + reason（供高亮/呈现），
// 不含任何执行指令。accept 走 diagnose.go applyInsight —— runbook_suggestion 无实际应用动作，
// 直接返回 accepted 终态，绝不调用 runbook 引擎。执行仍须响应者显式走 Runbook 端点（两档安全）。
func (e *CopilotEngine) suggestRunbook(ctx context.Context, inc *ent.Incident) (*ent.AIInsight, error) {
	// 取当前 Incident 所属 Service 关联的候选 Runbook（推荐只在这些里选，不凭空造）。
	svc, err := inc.QueryService().Only(ctx)
	if err != nil {
		// 无 Service（未路由）→ 无候选 Runbook → 不产出（非错误，降级为不推荐）。
		return nil, nil
	}
	candidateRbs, err := svc.QueryRunbooks().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query service runbooks: %w", err)
	}
	if len(candidateRbs) == 0 {
		return nil, nil // Service 未关联 Runbook → 无可推荐对象
	}

	// 相似历史事件里执行过哪些 Runbook 步骤（作为「这类故障用过它」的加权 evidence）。
	// finder 为 nil 或检索失败时降级为「无历史痕迹」，仍可只按候选 Runbook 让 LLM 选。
	pastUses := e.collectPastRunbookUses(ctx, inc)
	candidates := buildRunbookCandidates(candidateRbs, pastUses)

	prompt := buildRunbookPrompt(inc, candidates)
	raw, err := e.provider.Complete(ctx, prompt)
	if err != nil {
		// 复用诊断链降级：LLM 调用失败不产出、不阻断（记日志供排查，不上抛）。
		metrics.LLMCalls.WithLabelValues("copilot", "error").Inc()
		slog.Warn("copilot runbook: llm call failed, degrading to no-suggestion",
			"incident_id", inc.ID, "error", err)
		return nil, nil
	}
	metrics.LLMCalls.WithLabelValues("copilot", "ok").Inc()

	recID, conf, reason := parseRunbookOutput(raw)
	// 推荐的 Runbook 必须在候选集内（防 LLM 幻觉编造 id），且置信度达门槛。
	rec := findCandidate(candidates, recID)
	if rec == nil || conf < e.confidenceThreshold {
		return nil, nil
	}

	// evidence：被推荐的 Runbook（名称）+ 相似历史事件里用过它的痕迹（可溯源）。
	evidence := runbookEvidence(rec)
	if len(evidence) == 0 {
		return nil, nil // 双保险：无 evidence 不产出
	}

	insight, err := e.db.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageCopilot).
		SetType(aiinsight.TypeRunbookSuggestion).
		// content 只含推荐对象 + 理由（供高亮/呈现）。★ 不含执行指令：
		// accept 不触发执行，执行仍走 Runbook 两档安全（AI 推荐不绕过审批）。
		SetContent(map[string]any{
			"recommended_runbook_id":   rec.rb.ID,
			"recommended_runbook_name": rec.rb.Name,
			"reason":                   reason,
			// 明确提示：采纳仅高亮该 Runbook，执行仍须响应者走 Runbook 端点（写操作 require_approval）。
			"note": "采纳仅呈现/高亮该 Runbook，执行仍须显式操作并遵循两档安全（写操作须审批）",
		}).
		SetConfidence(conf).
		SetEvidence(evidence).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save runbook insight: %w", err)
	}

	e.recordInsightTimeline(ctx, inc.ID, insight.ID,
		fmt.Sprintf("AI 处置推荐（置信度 %.2f）：建议参考 Runbook「%s」", conf, rec.rb.Name),
		map[string]any{"insight_id": insight.ID, "confidence": conf,
			"recommended_runbook_id": rec.rb.ID, "recommended_runbook_name": rec.rb.Name})
	return insight, nil
}

// draftSummary 产出 draft_summary 建议：AI 草拟 Incident 当前状态摘要（供快速了解 / 交接）。
//
// evidence = 时间线条目（草拟依据，可溯源）。无时间线时不产出（无 evidence）。
// 与 postmortem_draft 区分：这是「处理中的实时状态摘要」（stage=copilot），
// 不是复盘全文（stage=postmortem，走 postmortem 引擎，不在本任务）。
func (e *CopilotEngine) draftSummary(ctx context.Context, inc *ent.Incident) (*ent.AIInsight, error) {
	items, err := e.db.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(inc.ID))).
		Order(ent.Asc(timelineitem.FieldTimestamp)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query timeline: %w", err)
	}
	if len(items) == 0 {
		return nil, nil // 无时间线 → 无 evidence → 不产出
	}

	prompt := buildSummaryPrompt(inc, items)
	raw, err := e.provider.Complete(ctx, prompt)
	if err != nil {
		metrics.LLMCalls.WithLabelValues("copilot", "error").Inc()
		slog.Warn("copilot summary: llm call failed, degrading to no-suggestion",
			"incident_id", inc.ID, "error", err)
		return nil, nil
	}
	metrics.LLMCalls.WithLabelValues("copilot", "ok").Inc()

	summary := strings.TrimSpace(raw)
	if summary == "" {
		return nil, nil // LLM 返回空 → 不产出
	}

	// evidence：时间线条目（草拟依据）。
	evidence := summaryEvidence(items)

	insight, err := e.db.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageCopilot).
		SetType(aiinsight.TypeDraftSummary).
		SetContent(map[string]any{"summary": summary}).
		// 摘要草拟是文本产出，无结构化置信度；置 1.0（人校对即用，不做门槛过滤）。
		SetConfidence(1).
		SetEvidence(evidence).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save summary insight: %w", err)
	}

	e.recordInsightTimeline(ctx, inc.ID, insight.ID,
		"AI 摘要草拟：已生成 Incident 当前状态摘要（供快速了解/交接）",
		map[string]any{"insight_id": insight.ID})
	return insight, nil
}

// collectPastRunbookUses 从相似历史事件里收集 runbook_executed 时间线痕迹（推荐加权 evidence）。
// finder 为 nil / 检索失败 / 无相似事件时返回空（降级：无历史痕迹，仍可只按候选 Runbook 推荐）。
func (e *CopilotEngine) collectPastRunbookUses(ctx context.Context, inc *ent.Incident) []pastRunbookUse {
	if e.finder == nil {
		return nil
	}
	similar, err := e.finder.FindSimilar(ctx, inc.ID, copilotSimilarLimit)
	if err != nil || len(similar) == 0 {
		return nil
	}
	var uses []pastRunbookUse
	for _, s := range similar {
		if s.ID == inc.ID {
			continue // 排除自身（检索本已排除，双保险）
		}
		items, err := e.db.TimelineItem.Query().
			Where(
				timelineitem.HasIncidentWith(incident.IDEQ(s.ID)),
				timelineitem.TypeEQ(timelineitem.TypeRunbookExecuted),
			).All(ctx)
		if err != nil {
			continue
		}
		for _, it := range items {
			// 只收「已执行」痕迹（跳过被阻断的 blocked=true 记录：那是未获批阻断，不代表用过）。
			if blocked, _ := it.Detail["blocked"].(bool); blocked {
				continue
			}
			step, _ := it.Detail["step"].(string)
			uses = append(uses, pastRunbookUse{
				incidentID: s.ID, incidentNumber: s.Number, stepName: step,
			})
		}
	}
	return uses
}

// recordInsightTimeline 写 ai_insight 时间线（best-effort）。actor.kind=ai、source=ai，
// 与诊断链/分诊 AI 一致，供时间线区分 AI 动作。recorder 为 nil 时跳过（降级/测试）。
func (e *CopilotEngine) recordInsightTimeline(ctx context.Context, incID, insightID int, content string, detail map[string]any) {
	if e.recorder == nil {
		return
	}
	_ = e.recorder.Record(ctx, incID, timelineitem.TypeAiInsight, content,
		timeline.Actor{Kind: "ai"}, timelineitem.SourceAi, detail)
}

// --- 候选构造 ---

// buildRunbookCandidates 把候选 Runbook 与相似历史执行痕迹关联起来（同一 Runbook 名匹配步骤痕迹为近似）。
// 历史痕迹按 Runbook 名近似归并（时间线只记 step 名，不记 runbook_id，故用名称/步骤归并作近似加权）。
func buildRunbookCandidates(rbs []*ent.Runbook, pastUses []pastRunbookUse) []runbookCandidate {
	out := make([]runbookCandidate, 0, len(rbs))
	for _, rb := range rbs {
		c := runbookCandidate{rb: rb}
		// 把相似历史里的执行痕迹都挂上（供 LLM 参考「这类故障历史上执行过处置」）。
		// 时间线不含 runbook_id，无法精确对应到具体 Runbook，故作为「同类历史处置痕迹」整体呈现。
		c.pastUses = pastUses
		out = append(out, c)
	}
	return out
}

// findCandidate 按 Runbook id 在候选集里查（推荐的 id 必须在候选集内，防幻觉）。
func findCandidate(candidates []runbookCandidate, rbID int) *runbookCandidate {
	for i := range candidates {
		if candidates[i].rb.ID == rbID {
			return &candidates[i]
		}
	}
	return nil
}

// --- prompt 构造 ---

// buildRunbookPrompt 构造处置推荐 prompt：列出候选 Runbook + 相似历史处置痕迹，让 LLM 选。
func buildRunbookPrompt(inc *ent.Incident, candidates []runbookCandidate) string {
	var sb strings.Builder
	sb.WriteString("你是运维处置助手。根据下面这个告警事件，从「候选处置手册」里推荐最合适的一个（若都不合适则不推荐）。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 只从候选处置手册里选，runbook_id 必须是候选里的 id；都不合适则 runbook_id=0、should_recommend=false\n")
	sb.WriteString("2. 用不确定性措辞（\"建议参考\"\"通常用\"），confidence 反映把握程度\n")
	sb.WriteString("3. 你只做推荐，不执行任何操作\n")
	sb.WriteString("4. 输出必须是 JSON：{\"should_recommend\":true|false,\"runbook_id\":<id>,\"confidence\":0.0-1.0,\"reason\":\"...\"}\n\n")
	fmt.Fprintf(&sb, "事件：[%s] %s —— %s\n\n", string(inc.Severity), inc.Title, inc.Summary)
	sb.WriteString("候选处置手册：\n")
	for _, c := range candidates {
		fmt.Fprintf(&sb, "- id=%d 「%s」（%s）\n", c.rb.ID, c.rb.Name, string(c.rb.Type))
	}
	// 相似历史事件里执行过的处置痕迹（同类故障历史上怎么处置的）。
	if len(candidates) > 0 && len(candidates[0].pastUses) > 0 {
		sb.WriteString("\n相似历史事件的处置痕迹（供参考「这类故障历史上执行过哪些处置」）：\n")
		for _, u := range candidates[0].pastUses {
			fmt.Fprintf(&sb, "- 事件 %s 执行过步骤：%s\n", u.incidentNumber, u.stepName)
		}
	}
	return sb.String()
}

// buildSummaryPrompt 构造摘要草拟 prompt：基于事件信息 + 时间线草拟当前状态摘要。
func buildSummaryPrompt(inc *ent.Incident, items []*ent.TimelineItem) string {
	var sb strings.Builder
	sb.WriteString("你是运维协同助手。根据以下事件信息与时间线，草拟一段「当前状态摘要」，供响应者快速了解进展或交接。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 客观陈述当前状态、已做处置、待办，不臆测未发生的事\n")
	sb.WriteString("2. 简洁，控制在数句话内\n")
	sb.WriteString("3. 直接输出摘要文本，不要额外解释\n\n")
	fmt.Fprintf(&sb, "事件：[%s] %s —— %s\n", string(inc.Severity), inc.Title, inc.Summary)
	fmt.Fprintf(&sb, "当前状态：%s\n\n", string(inc.Status))
	sb.WriteString("时间线：\n")
	for _, it := range items {
		fmt.Fprintf(&sb, "- [%s] %s: %s\n",
			it.Timestamp.Format("15:04"), string(it.Type), it.Content)
	}
	return sb.String()
}

// --- LLM 输出解析 ---

// parseRunbookOutput 解析处置推荐输出（JSON）。not should_recommend / 非 JSON 时返回 id=0（不产出）。
func parseRunbookOutput(raw string) (runbookID int, conf float32, reason string) {
	var out struct {
		ShouldRecommend bool    `json:"should_recommend"`
		RunbookID       int     `json:"runbook_id"`
		Confidence      float32 `json:"confidence"`
		Reason          string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return 0, 0, "" // 非 JSON → 不产出（推荐要结构化 id）
	}
	if !out.ShouldRecommend {
		return 0, 0, "" // LLM 判断无合适 Runbook → 不产出
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return out.RunbookID, out.Confidence, strings.TrimSpace(out.Reason)
}

// --- evidence 构造 ---

// runbookEvidence 用被推荐的 Runbook（名称）+ 相似历史处置痕迹作 runbook 推荐的 evidence。
func runbookEvidence(rec *runbookCandidate) []map[string]any {
	ev := make([]map[string]any, 0, 1+len(rec.pastUses))
	// 被推荐的 Runbook 本身作为首条 evidence（响应者可核对推荐对象）。
	ev = append(ev, map[string]any{
		"kind":         "runbook",
		"runbook_id":   rec.rb.ID,
		"runbook_name": rec.rb.Name,
		"runbook_type": string(rec.rb.Type),
	})
	// 相似历史事件的处置痕迹（「这类故障历史上执行过处置」可溯源）。
	for _, u := range rec.pastUses {
		ev = append(ev, map[string]any{
			"kind":            "similar_incident_runbook",
			"incident_id":     u.incidentID,
			"incident_number": u.incidentNumber,
			"step":            u.stepName,
		})
	}
	return ev
}

// summaryEvidence 用时间线条目作 draft_summary 的 evidence（草拟依据，可溯源）。
func summaryEvidence(items []*ent.TimelineItem) []map[string]any {
	ev := make([]map[string]any, 0, len(items))
	for _, it := range items {
		ev = append(ev, map[string]any{
			"kind":      "timeline",
			"timestamp": it.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			"type":      string(it.Type),
			"content":   it.Content,
		})
	}
	return ev
}
