// triage_ai.go 分诊阶段 AI：在告警分诊阶段产出「带 evidence 的建议」，落 AIInsight。
//
// 对应 docs/capabilities/07-timeline-ai.md §B2（分诊阶段的 AI 介入）与 roadmap T3.2 / 审计 C15：
//   - severity_adjustment：基于 Incident + 关联 Event 让 LLM 判断当前严重度是否偏高/偏低，
//     产出建议调整值（带 evidence + 置信度）。accept 走 T3.1 的 applied 路径真正改严重度。
//   - dedup_suggestion：借助相似检索找出可能同根因的多个 Incident，让 LLM 判断是否建议合并，
//     产出合并建议（带 evidence 列出候选单）。实际合并依赖 merge 端点（审计 M7，未实现），
//     故本阶段只产出「建议 + 展示」，accept 仅置 accepted（合并执行待 merge 端点）。
//
// 全程遵循 §B1 设计原则与本任务基线：
//   - human-in-the-loop：产出 status=suggested，须人 accept/reject 才生效。
//   - evidence 强制：无 evidence 不产出建议（保证可溯源、可信）。
//   - 置信度门槛：低于阈值（默认 0.6，Q2）的建议不产出。
//   - 可降级：LLM 不可用 / 调用失败 → 不产出建议，分诊主流程继续不阻断
//     （复用诊断链同款降级语义，见 diagnose.go 的 FIX-C）。
//   - 触发异步：由 triage 建单后异步调用（不阻塞分诊主流程）或经手动端点触发。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/timeline"
)

// defaultConfidenceThreshold 建议产出的置信度门槛（capabilities/07 §开放问题 Q2）。
// 低于此值的 LLM 建议不产出——低置信度建议打扰响应者、拉低 AI 可信度。
const defaultConfidenceThreshold float32 = 0.6

// dedupCandidateLimit 相似检索取候选合并单的上限（dedup 建议）。
const dedupCandidateLimit = 5

// SimilarFinder 相似 Incident 检索器抽象（dedup 建议用）。
// 由 *DiagnoseEngine 实现（FindSimilar）——分诊 AI 复用诊断链已有的 pgvector/LIKE 检索能力，
// 不重复造检索逻辑。抽象成接口而非直接持有 *DiagnoseEngine，便于测试注入桩。
type SimilarFinder interface {
	FindSimilar(ctx context.Context, incID, limit int) ([]*ent.Incident, error)
}

// TriageAIEngine 分诊阶段 AI 引擎。产出 severity_adjustment / dedup_suggestion 建议，落 AIInsight。
// 与 DiagnoseEngine 同款结构：持有 db + provider + recorder，provider 不可用时全程降级不产出。
type TriageAIEngine struct {
	db       *ent.Client
	provider Provider // LLM 提供方，nil 或不可用时降级（不产出建议）
	// finder 相似检索器（dedup 建议用）。nil 时 dedup 建议降级为不产出（无候选可判）。
	finder SimilarFinder
	// recorder 时间线记录器。产出 AIInsight 后写 ai_insight 时间线（全程留痕）。
	// 为 nil 时跳过（降级/测试），不阻塞产出主流程。
	recorder *timeline.Recorder
	// confidenceThreshold 建议产出置信度门槛（默认 0.6）。
	confidenceThreshold float32
}

// NewTriageAIEngine 创建分诊 AI 引擎。置信度门槛用默认 0.6，可经 SetConfidenceThreshold 覆盖。
func NewTriageAIEngine(db *ent.Client, p Provider) *TriageAIEngine {
	return &TriageAIEngine{db: db, provider: p, confidenceThreshold: defaultConfidenceThreshold}
}

// SetSimilarFinder 注入相似检索器（dedup 建议用）。装配时传入 *DiagnoseEngine。
func (e *TriageAIEngine) SetSimilarFinder(f SimilarFinder) { e.finder = f }

// SetRecorder 注入时间线记录器（装配时调用）：产出 AI 洞察后写 ai_insight 时间线。
func (e *TriageAIEngine) SetRecorder(r *timeline.Recorder) { e.recorder = r }

// SetConfidenceThreshold 覆盖置信度门槛（<=0 时保留默认，避免误配为 0 使一切建议都产出）。
func (e *TriageAIEngine) SetConfidenceThreshold(t float32) {
	if t > 0 {
		e.confidenceThreshold = t
	}
}

// available 是否可产出 AI 建议（LLM 配置且可用）。不可用时调用方降级不产出。
func (e *TriageAIEngine) available() bool {
	return e.provider != nil && e.provider.Available()
}

// TriageResult 分诊 AI 一次运行的产出（可能同时含 severity 与 dedup 两类建议，均可能为 nil）。
type TriageResult struct {
	Severity *ent.AIInsight `json:"severity,omitempty"` // severity_adjustment 建议（未产出为 nil）
	Dedup    *ent.AIInsight `json:"dedup,omitempty"`    // dedup_suggestion 建议（未产出为 nil）
}

// AnalyzeIncident 对一个 Incident 跑分诊 AI 全流程：severity 建议 + dedup 建议。
// 是 triage 建单后异步触发与手动端点共用的入口。
//
// 降级：LLM 不可用时直接返回空结果（两建议均 nil），不报错、不落 AIInsight——
// 保证分诊主流程不被 AI 阻断（触发方 best-effort 调用，忽略/记日志即可）。
// 单类建议内部失败（LLM 报错 / 无 evidence / 低置信度）不影响另一类，各自降级为不产出。
func (e *TriageAIEngine) AnalyzeIncident(ctx context.Context, incID int) (*TriageResult, error) {
	res := &TriageResult{}
	if !e.available() {
		return res, nil // 降级：无 LLM，不产出任何建议
	}
	// incident 不存在归一为 error（手动端点据此返 404）；异步触发方忽略即可。
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, err
	}

	// severity 建议（单类失败不影响 dedup）
	if sev, serr := e.suggestSeverity(ctx, inc); serr != nil {
		slog.Warn("triage ai: severity suggestion failed, skip", "incident_id", incID, "error", serr)
	} else {
		res.Severity = sev
	}

	// dedup 建议（单类失败不影响返回）
	if dd, derr := e.suggestDedup(ctx, inc); derr != nil {
		slog.Warn("triage ai: dedup suggestion failed, skip", "incident_id", incID, "error", derr)
	} else {
		res.Dedup = dd
	}

	return res, nil
}

// suggestSeverity 产出 severity_adjustment 建议：LLM 判断当前严重度是否应调整。
//
// evidence = 关联 Event 的摘要（LLM 判断的事实依据）。无 Event 时不产出（无 evidence）。
// 建议目标严重度非法 / 与当前一致 / 置信度不足时不产出（避免噪声与无效建议）。
// 产出的 content 含 target_severity —— accept 时 T3.1 的 applyInsight 据此真正改严重度（走 applied）。
func (e *TriageAIEngine) suggestSeverity(ctx context.Context, inc *ent.Incident) (*ent.AIInsight, error) {
	// 取关联 Event 作为判断依据（也是 evidence 来源）。无 Event 无依据 → 不产出。
	events, err := e.db.Event.Query().
		Where(event.HasIncidentWith(incident.IDEQ(inc.ID))).
		Order(ent.Asc(event.FieldCreatedAt)).
		Limit(20).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	if len(events) == 0 {
		return nil, nil // 无 evidence，不产出（基线：无 evidence 不产生）
	}

	prompt := buildSeverityPrompt(inc, events)
	raw, err := e.provider.Complete(ctx, prompt)
	if err != nil {
		// 复用诊断链降级：LLM 调用失败不产出、不阻断（记日志供排查，不上抛）。
		metrics.LLMCalls.WithLabelValues("triage", "error").Inc()
		slog.Warn("triage ai severity: llm call failed, degrading to no-suggestion",
			"incident_id", inc.ID, "error", err)
		return nil, nil
	}
	metrics.LLMCalls.WithLabelValues("triage", "ok").Inc()

	target, conf, reason := parseSeverityOutput(raw)
	// 目标严重度必须合法且与当前不同——否则没有「调整」可言，不产出。
	if !isValidSeverity(target) || target == string(inc.Severity) {
		return nil, nil
	}
	// 置信度门槛：低于阈值不产出（Q2）。
	if conf < e.confidenceThreshold {
		return nil, nil
	}

	// evidence：关联 Event 摘要（可溯源）。
	evidence := severityEvidence(events)
	if len(evidence) == 0 {
		return nil, nil // 双保险：无 evidence 不产出
	}

	insight, err := e.db.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageTriage).
		SetType(aiinsight.TypeSeverityAdjustment).
		// content 带 target_severity —— accept 走 T3.1 applyInsight 真正改严重度（applied）。
		SetContent(map[string]any{
			"target_severity":  target,
			"current_severity": string(inc.Severity),
			"reason":           reason,
		}).
		SetConfidence(conf).
		SetEvidence(evidence).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save severity insight: %w", err)
	}

	e.recordInsightTimeline(ctx, inc.ID, insight.ID,
		fmt.Sprintf("AI 严重度建议（置信度 %.2f）：%s → %s", conf, string(inc.Severity), target),
		map[string]any{"insight_id": insight.ID, "confidence": conf,
			"from": string(inc.Severity), "to": target})
	return insight, nil
}

// suggestDedup 产出 dedup_suggestion 建议：识别可能同根因、可合并的其它 Incident。
//
// 复用相似检索（finder）取候选活跃 Incident，让 LLM 判断是否建议合并。
// 无 finder / 无候选 / LLM 判断不合并 / 置信度不足时不产出。
// evidence = 候选 Incident（编号+标题），使响应者可核对「建议合并哪些单」。
// 实际合并依赖 merge 端点（审计 M7 未实现）——accept 仅置 accepted（合并执行待 merge 端点）。
func (e *TriageAIEngine) suggestDedup(ctx context.Context, inc *ent.Incident) (*ent.AIInsight, error) {
	if e.finder == nil {
		return nil, nil // 无检索器，无候选可判 → 不产出
	}
	candidates, err := e.finder.FindSimilar(ctx, inc.ID, dedupCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
	}
	// 只保留仍活跃的候选（已 resolved/closed 的单合并无意义），并排除自身（检索本已排除，双保险）。
	active := filterActiveCandidates(candidates, inc.ID)
	if len(active) == 0 {
		return nil, nil // 无候选 → 无 evidence → 不产出
	}

	prompt := buildDedupPrompt(inc, active)
	raw, err := e.provider.Complete(ctx, prompt)
	if err != nil {
		metrics.LLMCalls.WithLabelValues("triage", "error").Inc()
		slog.Warn("triage ai dedup: llm call failed, degrading to no-suggestion",
			"incident_id", inc.ID, "error", err)
		return nil, nil
	}
	metrics.LLMCalls.WithLabelValues("triage", "ok").Inc()

	shouldMerge, conf, mergeIDs, reason := parseDedupOutput(raw, active)
	if !shouldMerge || conf < e.confidenceThreshold || len(mergeIDs) == 0 {
		return nil, nil // LLM 判断不合并 / 置信度不足 / 未指出具体单 → 不产出
	}

	// evidence：LLM 建议合并的候选单（编号+标题），可溯源。
	evidence := dedupEvidence(active, mergeIDs)
	if len(evidence) == 0 {
		return nil, nil // 无 evidence 不产出
	}

	insight, err := e.db.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageTriage).
		SetType(aiinsight.TypeDedupSuggestion).
		SetContent(map[string]any{
			"merge_candidate_ids": mergeIDs,
			"reason":              reason,
			// 提示合并执行待 merge 端点（M7 未实现）：accept 仅置 accepted，不自动合并。
			"note": "合并执行待 merge 端点（M7），当前 accept 仅记录采纳",
		}).
		SetConfidence(conf).
		SetEvidence(evidence).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save dedup insight: %w", err)
	}

	e.recordInsightTimeline(ctx, inc.ID, insight.ID,
		fmt.Sprintf("AI 合并建议（置信度 %.2f）：疑似 %d 个同根因单可合并", conf, len(mergeIDs)),
		map[string]any{"insight_id": insight.ID, "confidence": conf, "merge_candidate_ids": mergeIDs})
	return insight, nil
}

// recordInsightTimeline 写 ai_insight 时间线（best-effort）。actor.kind=ai、source=ai，
// 与诊断链一致，供时间线区分 AI 动作。recorder 为 nil 时跳过（降级/测试）。
func (e *TriageAIEngine) recordInsightTimeline(ctx context.Context, incID, insightID int, content string, detail map[string]any) {
	if e.recorder == nil {
		return
	}
	_ = e.recorder.Record(ctx, incID, timelineitem.TypeAiInsight, content,
		timeline.Actor{Kind: "ai"}, timelineitem.SourceAi, detail)
}

// --- prompt 构造 ---

// buildSeverityPrompt 构造严重度建议 prompt。要求不确定性措辞 + JSON 输出。
func buildSeverityPrompt(inc *ent.Incident, events []*ent.Event) string {
	var sb strings.Builder
	sb.WriteString("你是运维分诊助手。判断下面这个告警事件的严重度是否设置合理，是否应调高或调低。\n")
	sb.WriteString("严重度枚举：critical（严重）> warning（警告）> info（提示）。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 只在有明确依据时才建议调整，把握不大时保持当前严重度\n")
	sb.WriteString("2. 用不确定性措辞（\"建议\"\"疑似\"），confidence 反映把握程度\n")
	sb.WriteString("3. 输出必须是 JSON：{\"target_severity\":\"critical|warning|info\",\"confidence\":0.0-1.0,\"reason\":\"...\"}\n")
	sb.WriteString("   若认为当前严重度合理无需调整，target_severity 填当前值即可。\n\n")
	fmt.Fprintf(&sb, "当前严重度：%s\n", string(inc.Severity))
	fmt.Fprintf(&sb, "标题：%s\n", inc.Title)
	fmt.Fprintf(&sb, "概要：%s\n\n", inc.Summary)
	sb.WriteString("关联告警：\n")
	for _, ev := range events {
		fmt.Fprintf(&sb, "- [%s] %s\n", string(ev.Severity), ev.Summary)
	}
	return sb.String()
}

// buildDedupPrompt 构造合并建议 prompt。列出候选单让 LLM 判断哪些同根因可合并。
func buildDedupPrompt(inc *ent.Incident, candidates []*ent.Incident) string {
	var sb strings.Builder
	sb.WriteString("你是运维分诊助手。判断下面的「目标事件」与「候选事件」中，哪些可能是同一根因、建议合并。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 只在确有同根因迹象时才建议合并，无关的不要合并\n")
	sb.WriteString("2. merge_ids 只填候选事件里应合并的那些 id；无可合并则填空数组、should_merge=false\n")
	sb.WriteString("3. 输出必须是 JSON：{\"should_merge\":true|false,\"merge_ids\":[id,...],\"confidence\":0.0-1.0,\"reason\":\"...\"}\n\n")
	fmt.Fprintf(&sb, "目标事件：#%d [%s] %s —— %s\n\n", inc.ID, string(inc.Severity), inc.Title, inc.Summary)
	sb.WriteString("候选事件：\n")
	for _, c := range candidates {
		fmt.Fprintf(&sb, "- id=%d [%s] %s —— %s\n", c.ID, string(c.Severity), c.Title, c.Summary)
	}
	return sb.String()
}

// --- LLM 输出解析 ---

// parseSeverityOutput 解析严重度建议输出（JSON）。失败返回空 target（调用方据此不产出）。
func parseSeverityOutput(raw string) (target string, conf float32, reason string) {
	var out struct {
		TargetSeverity string  `json:"target_severity"`
		Confidence     float32 `json:"confidence"`
		Reason         string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return "", 0, "" // 非 JSON → 不产出（严重度建议要结构化 target，纯文本无法采纳）
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return strings.TrimSpace(out.TargetSeverity), out.Confidence, strings.TrimSpace(out.Reason)
}

// parseDedupOutput 解析合并建议输出（JSON）。只保留出现在候选集里的 id（防 LLM 幻觉编造 id）。
func parseDedupOutput(raw string, candidates []*ent.Incident) (shouldMerge bool, conf float32, mergeIDs []int, reason string) {
	var out struct {
		ShouldMerge bool    `json:"should_merge"`
		MergeIDs    []int   `json:"merge_ids"`
		Confidence  float32 `json:"confidence"`
		Reason      string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return false, 0, nil, ""
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	// 只保留候选集内的 id（LLM 可能编造不存在的 id，过滤防幻觉）。
	valid := make(map[int]bool, len(candidates))
	for _, c := range candidates {
		valid[c.ID] = true
	}
	for _, id := range out.MergeIDs {
		if valid[id] {
			mergeIDs = append(mergeIDs, id)
		}
	}
	return out.ShouldMerge, out.Confidence, mergeIDs, strings.TrimSpace(out.Reason)
}

// --- evidence 构造 ---

// severityEvidence 用关联 Event 摘要作 severity 建议的 evidence（可溯源事实依据）。
func severityEvidence(events []*ent.Event) []map[string]any {
	ev := make([]map[string]any, 0, len(events))
	for _, e := range events {
		ev = append(ev, map[string]any{
			"kind":     "event",
			"event_id": e.ID,
			"severity": string(e.Severity),
			"summary":  e.Summary,
		})
	}
	return ev
}

// dedupEvidence 用被建议合并的候选单（编号+标题）作 dedup 建议的 evidence。
// 只收录 mergeIDs 里的候选（LLM 实际指出的合并对象），使响应者可核对。
func dedupEvidence(candidates []*ent.Incident, mergeIDs []int) []map[string]any {
	want := make(map[int]bool, len(mergeIDs))
	for _, id := range mergeIDs {
		want[id] = true
	}
	ev := make([]map[string]any, 0, len(mergeIDs))
	for _, c := range candidates {
		if !want[c.ID] {
			continue
		}
		ev = append(ev, map[string]any{
			"kind":        "incident",
			"incident_id": c.ID,
			"number":      c.Number,
			"title":       c.Title,
			"severity":    string(c.Severity),
		})
	}
	return ev
}

// --- helpers ---

// filterActiveCandidates 保留仍活跃（triggered/escalated/acked）的候选并排除自身。
// 已 resolved/closed 的单合并无意义。
func filterActiveCandidates(candidates []*ent.Incident, selfID int) []*ent.Incident {
	out := make([]*ent.Incident, 0, len(candidates))
	for _, c := range candidates {
		if c.ID == selfID {
			continue
		}
		switch c.Status {
		case incident.StatusTriggered, incident.StatusEscalated, incident.StatusAcked:
			out = append(out, c)
		}
	}
	return out
}

// isValidSeverity 校验字符串是否合法严重度枚举。
func isValidSeverity(s string) bool {
	switch s {
	case string(incident.SeverityCritical), string(incident.SeverityWarning), string(incident.SeverityInfo):
		return true
	default:
		return false
	}
}
