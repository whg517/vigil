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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/pgvector/pgvector-go"
)

// SQLRunner 执行一条 raw SQL 查询，遍历行交给 scan 回调（pgvector 距离检索用）。
// 由 main 注入（包 *sql.DB），避免 ai 包直接依赖 ent driver 内部。
// 为 nil 时 FindSimilar 的 pgvector 路径降级为 LIKE 文本匹配。
type SQLRunner func(ctx context.Context, query string, args []any, scan func(rows *sql.Rows) error) error

// DiagnoseEngine AI 诊断引擎。
type DiagnoseEngine struct {
	db       *ent.Client
	provider Provider // LLM 提供方，nil 或不可用时降级（不诊断）
	// runSQL raw SQL 执行器（pgvector 相似检索用）；nil 时降级为 LIKE 匹配。
	runSQL SQLRunner
}

// NewDiagnoseEngine 创建诊断引擎。
func NewDiagnoseEngine(db *ent.Client, p Provider) *DiagnoseEngine {
	return &DiagnoseEngine{db: db, provider: p}
}

// SetSQLRunner 注入 raw SQL 执行器（pgvector 相似检索用）。
func (e *DiagnoseEngine) SetSQLRunner(r SQLRunner) { e.runSQL = r }

// DiagnoseResult 诊断结果。
// json tag 必填：Go 默认序列化为 PascalCase（InsightID/RootCause），
// 而 OpenAPI spec 经 swag 生成的 schema 期望 snake_case（insight_id/root_cause），
// 不加 tag 会导致前端按 spec 取不到字段。
type DiagnoseResult struct {
	InsightID  int              `json:"insight_id"` // AIInsight ID
	RootCause  string           `json:"root_cause"` // 根因线索文本
	Confidence float32          `json:"confidence"` // 置信度
	Evidence   []map[string]any `json:"evidence"`   // 依据
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
		// FIX-C：LLM 调用失败（401/超时/限流等）降级为不诊断（返回 nil），
		// 而非向上抛 500。符合设计基线第 7 条（凭证/服务不可用时降级）。
		// handler 见 nil 会返回 200 disabled，让前端走规则兜底（与未配置 LLM 一致）。
		// 记日志保留失败原因供运维排查（不泄露给前端）。
		slog.Warn("ai diagnose: llm call failed, degrading to disabled",
			"incident_id", incID, "error", err)
		return nil, nil
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

// FindSimilar 检索相似历史事件（能力域 11 M11.4）。
//
// 主路径：pgvector 语义检索。
//  1. 取 incident.embedding；为空则懒计算（LLM Embed 标题+摘要）并回写持久化（避免重复 embed）
//  2. 用 raw SQL 余弦距离 <=> 排序，排除自身
//
// 降级路径：pgvector 不可用（无扩展/sqlite 测试/Embed 失败）→ 回退 LIKE 文本匹配。
func (e *DiagnoseEngine) FindSimilar(ctx context.Context, incID int, limit int) ([]*ent.Incident, error) {
	if limit <= 0 {
		limit = 5
	}
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, err
	}

	// 尝试 pgvector 语义检索
	if results, err := e.findSimilarVector(ctx, inc, limit); err == nil && results != nil {
		return results, nil
	}
	// 降级：LIKE 文本匹配（capabilities/07 §B4 兜底）
	return e.findSimilarText(ctx, inc, incID, limit)
}

// findSimilarVector pgvector 语义检索。任一步失败返回 error 让上层降级。
func (e *DiagnoseEngine) findSimilarVector(ctx context.Context, inc *ent.Incident, limit int) ([]*ent.Incident, error) {
	if e.provider == nil || !e.provider.Available() {
		return nil, fmt.Errorf("provider unavailable")
	}
	if e.runSQL == nil {
		return nil, fmt.Errorf("sql runner not configured")
	}
	// 取/算 embedding
	vec, err := e.ensureEmbedding(ctx, inc)
	if err != nil || len(vec) == 0 {
		return nil, fmt.Errorf("ensure embedding: %w", err)
	}
	// raw SQL 余弦距离排序（pgvector <=> 操作符）
	// vector 字面量格式：'[0.1,0.2,...]'，与 pgvector.Vector.String() 一致
	const q = `SELECT id FROM incidents
			   WHERE embedding IS NOT NULL AND id <> $1
			   ORDER BY embedding <=> $2::vector
			   LIMIT $3`
	var ids []int
	scanErr := e.runSQL(ctx, q, []any{inc.ID, vectorLiteral(vec), limit}, func(rows *sql.Rows) error {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if scanErr != nil {
		return nil, fmt.Errorf("pgvector query: %w", scanErr)
	}
	if len(ids) == 0 {
		return []*ent.Incident{}, nil
	}
	return e.db.Incident.Query().Where(incident.IDIn(ids...)).All(ctx)
}

// ensureEmbedding 确保 incident 有 embedding：为空则 Embed 并回写持久化（懒计算）。
func (e *DiagnoseEngine) ensureEmbedding(ctx context.Context, inc *ent.Incident) ([]float32, error) {
	if inc.Embedding != nil && inc.Embedding.Valid {
		return inc.Embedding.Slice(), nil
	}
	// LLM embed：标题 + 摘要拼接（截断避免超 token）
	text := strings.TrimSpace(inc.Title)
	if inc.Summary != "" {
		text += " " + inc.Summary
	}
	if len(text) > 2000 {
		text = text[:2000]
	}
	vec, err := e.provider.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	// 回写持久化（schema.NullableVector）
	nv := &schema.NullableVector{Valid: true}
	nv.Vector = pgvector.NewVector(vec)
	if err := e.db.Incident.UpdateOneID(inc.ID).SetEmbedding(nv).Exec(ctx); err != nil {
		// 回写失败不阻塞检索（用内存中的 vec 继续）
		_ = err
	}
	return vec, nil
}

// FindSimilarPostmortems 检索与指定 incident 相似的已发布复盘（知识沉淀 M12.6）。
// 用 incident 的 embedding 在 postmortems.embedding 上做余弦距离检索。
// 用于"上次类似故障是怎么处理的"——published 复盘反哺新事件诊断。
// pgvector/Embed 不可用时返回空切片（降级，不阻塞诊断）。
func (e *DiagnoseEngine) FindSimilarPostmortems(ctx context.Context, incID int, limit int) ([]*ent.Postmortem, error) {
	if limit <= 0 {
		limit = 3
	}
	inc, err := e.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, err
	}
	// 复用 incident 的 embedding（确保已计算）
	vec, err := e.ensureEmbedding(ctx, inc)
	if err != nil || len(vec) == 0 {
		return []*ent.Postmortem{}, nil // 降级：无 embedding 无法语义检索
	}
	if e.runSQL == nil {
		return []*ent.Postmortem{}, nil
	}
	// 检索 published 且有 embedding 的复盘，按余弦距离排序
	const q = `SELECT id FROM postmortems
			   WHERE embedding IS NOT NULL AND status = 'published'
			   ORDER BY embedding <=> $1::vector
			   LIMIT $2`
	var ids []int
	scanErr := e.runSQL(ctx, q, []any{vectorLiteral(vec), limit}, func(rows *sql.Rows) error {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if scanErr != nil || len(ids) == 0 {
		return []*ent.Postmortem{}, nil
	}
	return e.db.Postmortem.Query().Where(postmortem.IDIn(ids...)).WithIncident().All(ctx)
}

// vectorLiteral 把 []float32 转成 pgvector 文本字面量 '[0.1,0.2,...]'。
func vectorLiteral(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

// findSimilarText 降级路径：LIKE 文本匹配（原实现）。
func (e *DiagnoseEngine) findSimilarText(ctx context.Context, inc *ent.Incident, incID, limit int) ([]*ent.Incident, error) {
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

// ErrInsightAlreadyResolved AI 建议已被改判（非 suggested），拒绝再次改判（S11 状态前置校验）。
// 一条建议只应被 accept/reject 一次——否则可 accepted↔rejected 反复翻转，
// 破坏 human-in-the-loop 决策的可信度与审计留痕的准确性。handler 据此返回 409。
var ErrInsightAlreadyResolved = errors.New("ai insight already resolved")

// ResolveInsight 人确认/拒绝 AI 建议（human-in-the-loop，S11）。
// accepted=true → status=accepted，false → rejected。
//
// 前置校验：仅当当前 status=suggested 时才允许改判；已 resolved（accepted/rejected/applied）
// 的再改判返回 ErrInsightAlreadyResolved（防反复翻转）。
// 改判时留痕 resolved_by/resolved_at（谁在何时改判），供审计追溯。
// actorID<=0（匿名/渐进阶段）时不写 resolved_by（避免记入伪造身份）。
func (e *DiagnoseEngine) ResolveInsight(ctx context.Context, insightID, actorID int, accepted bool) (*ent.AIInsight, error) {
	ins, err := e.db.AIInsight.Get(ctx, insightID)
	if err != nil {
		return nil, err
	}
	// 状态前置校验：只有 suggested 才可改判。
	if ins.Status != aiinsight.StatusSuggested {
		return nil, ErrInsightAlreadyResolved
	}
	st := aiinsight.StatusRejected
	if accepted {
		st = aiinsight.StatusAccepted
	}
	upd := e.db.AIInsight.UpdateOneID(insightID).
		SetStatus(st).
		SetResolvedAt(time.Now())
	if actorID > 0 {
		upd = upd.SetResolvedBy(actorID)
	}
	return upd.Save(ctx)
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
	fmt.Fprintf(&sb, "- 标题：%s\n", inc.Title)
	fmt.Fprintf(&sb, "- 严重度：%s\n", string(inc.Severity))
	fmt.Fprintf(&sb, "- 概要：%s\n\n", inc.Summary)
	sb.WriteString("时间线：\n")
	for _, it := range items {
		fmt.Fprintf(&sb, "- [%s] %s: %s\n",
			it.Timestamp.Format("15:04"), string(it.Type), it.Content)
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
