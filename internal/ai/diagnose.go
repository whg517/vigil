// diagnose.go AI 诊断引擎：生成根因线索 + 相似事件建议，落 AIInsight。
//
// 对应 docs/capabilities/07-timeline-ai.md §B2-B4：
// · root_cause_hint：基于事件 + 时间线，让 LLM 给根因线索（带不确定性措辞）
// · similar_incident：检索历史相似事件
// · 所有产出落 AIInsight，status=suggested，需人 accept/reject（human-in-the-loop）
// · 每条建议带 evidence（可溯源）
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
)

// DiagnoseEngine AI 诊断引擎。
type DiagnoseEngine struct {
	db       *ent.Client
	provider Provider // LLM 提供方，nil 或不可用时降级（不诊断）
}

// NewDiagnoseEngine 创建诊断引擎。
func NewDiagnoseEngine(db *ent.Client, p Provider) *DiagnoseEngine {
	return &DiagnoseEngine{db: db, provider: p}
}

// DiagnoseResult 诊断结果。
type DiagnoseResult struct {
	InsightID int      // AIInsight ID
	RootCause string   // 根因线索文本
	Confidence float32 // 置信度
	Evidence  []map[string]any // 依据
}

// Diagnose 对某事件做根因诊断，落 AIInsight（status=suggested）。
// 无 LLM 或不可用时返回 nil（降级，不诊断）。
func (e *DiagnoseEngine) Diagnose(ctx context.Context, incID int) (*DiagnoseResult, error) {
	if e.provider == nil || !e.provider.Available() {
		return nil, nil // 降级
	}

	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, fmt.Errorf("get incident: %w", err)
	}

	// 取时间线（诊断依据）
	items, err := e.db.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
		Order(ent.Asc(timelineitem.FieldTimestamp)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query timeline: %w", err)
	}

	// 构造诊断 prompt
	prompt := buildDiagnosePrompt(inc, items)
	raw, err := e.provider.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm diagnose: %w", err)
	}

	// 解析 LLM 输出（期望 JSON：{root_cause, confidence}）
	rc, conf := parseDiagnoseOutput(raw)

	// 构造 evidence（时间线条目作为依据）
	evidence := make([]map[string]any, 0, len(items))
	for _, it := range items {
		evidence = append(evidence, map[string]any{
			"timestamp": it.Timestamp.Format(time.RFC3339),
			"type":      string(it.Type),
			"content":   it.Content,
		})
	}

	// 落 AIInsight
	insight, err := e.db.AIInsight.Create().
		SetIncidentID(incID).
		SetStage(aiinsight.StageDiagnose).
		SetType(aiinsight.TypeRootCauseHint).
		SetContent(map[string]any{"root_cause": rc}).
		SetConfidence(conf).
		SetEvidence(evidence).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save ai insight: %w", err)
	}

	return &DiagnoseResult{
		InsightID:  insight.ID,
		RootCause:  rc,
		Confidence: conf,
		Evidence:   evidence,
	}, nil
}

// FindSimilar 检索相似历史事件（简化版：按标题/摘要文本匹配）。
// 向量化检索（pgvector）后续实现，当前用 LIKE 文本匹配。
func (e *DiagnoseEngine) FindSimilar(ctx context.Context, incID int, limit int) ([]*ent.Incident, error) {
	if limit <= 0 {
		limit = 5
	}
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, err
	}
	// 用标题关键词匹配历史事件（排除自身）
	keyword := extractKeyword(inc.Title)
	if keyword == "" {
		return nil, nil
	}
	return e.db.Incident.Query().
		Where(
			incident.IDNEQ(incID),
			incident.Or(
				incident.TitleContains(keyword),
				incident.SummaryContains(keyword),
			),
		).
		Order(ent.Desc(incident.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
}

// ResolveInsight 人确认/拒绝 AI 建议（human-in-the-loop）。
// accepted=true → status=accepted，false → rejected。
func (e *DiagnoseEngine) ResolveInsight(ctx context.Context, insightID int, accepted bool) error {
	st := aiinsight.StatusRejected
	if accepted {
		st = aiinsight.StatusAccepted
	}
	return e.db.AIInsight.UpdateOneID(insightID).SetStatus(st).Exec(ctx)
}

// buildDiagnosePrompt 构造根因诊断 prompt。
// 强制要求不确定性措辞 + JSON 输出格式。
func buildDiagnosePrompt(inc *ent.Incident, items []*ent.TimelineItem) string {
	var sb strings.Builder
	sb.WriteString("你是运维根因分析助手。根据以下事件信息，推测可能的根因。\n")
	sb.WriteString("要求：\n")
	sb.WriteString("1. 用不确定性措辞（\"可能\"\"疑似\"\"初步判断\"），绝不武断下结论\n")
	sb.WriteString("2. 输出必须是 JSON 格式：{\"root_cause\":\"...\",\"confidence\":0.0-1.0}\n\n")
	sb.WriteString("事件信息：\n")
	sb.WriteString(fmt.Sprintf("- 标题：%s\n", inc.Title))
	sb.WriteString(fmt.Sprintf("- 严重度：%s\n", string(inc.Severity)))
	sb.WriteString(fmt.Sprintf("- 概要：%s\n\n", inc.Summary))
	sb.WriteString("时间线：\n")
	for _, it := range items {
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n",
			it.Timestamp.Format("15:04"), string(it.Type), it.Content))
	}
	return sb.String()
}

// parseDiagnoseOutput 解析 LLM 输出（JSON），失败则降级为纯文本 + 低置信度。
func parseDiagnoseOutput(raw string) (string, float32) {
	var out struct {
		RootCause  string  `json:"root_cause"`
		Confidence float32 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err == nil && out.RootCause != "" {
		if out.Confidence > 1 {
			out.Confidence = 1
		}
		return out.RootCause, out.Confidence
	}
	// 降级：整个输出当作根因文本，低置信度
	return strings.TrimSpace(raw), 0.3
}

// extractKeyword 从标题提取关键词（简化：取首个有意义的词，按 rune 切分避免截断 UTF-8）。
// 真实实现后续用向量化检索替代。
func extractKeyword(title string) string {
	title = strings.TrimSpace(title)
	// 去掉常见 severity 前缀（保留原大小写给英文）
	low := strings.ToLower(title)
	for _, prefix := range []string{"[critical] ", "[warning] ", "[info] "} {
		if strings.HasPrefix(low, prefix) {
			title = title[len(prefix):]
			break
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	runes := []rune(title)
	// ASCII 取第一个空格前的词；中文取前 2 个字
	if isASCII(title) {
		if idx := strings.Index(title, " "); idx > 0 {
			return title[:idx]
		}
		return title
	}
	if len(runes) >= 2 {
		return string(runes[:2])
	}
	return title
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
