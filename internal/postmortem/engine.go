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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/event"

	"github.com/pgvector/pgvector-go"
)

// ErrPostmortemNotDraft 复盘已脱离 draft（in_review/published/archived），
// 拒绝重新起草覆盖（S7 覆盖保护）。已进入评审/发布的复盘 sections 是人工校对过的成果，
// 二次起草会无条件冲掉——handler 据此返回 409，让调用方感知"已存在不可覆盖"而非静默丢数据。
var ErrPostmortemNotDraft = errors.New("postmortem is not in draft status, refusing to overwrite")

// ErrPostmortemLocked 复盘已发布/归档，sections 已定稿锁定，拒绝逐段编辑（T4.2）。
//
// 为什么锁定 published/archived：published 复盘是组织知识库的定稿来源（M12.6 反哺相似检索，
// embedding 已入库），archived 是其归档态。允许发布后随意改段会让「已发布 = 定稿事实」失效，
// 且与 embedding 语义脱节。定稿后如需订正，走 archived 归档留存旧版，另起新复盘——而非原地改。
// draft/in_review 仍是评审进行时，逐段编辑正是本任务要开的口子。
var ErrPostmortemLocked = errors.New("postmortem is published/archived, sections are locked")

// ErrNoSections 逐段编辑请求未携带任何有效段落，无可更新（handler 据此返回 400）。
var ErrNoSections = errors.New("no sections to update")

// aiSectionsKey sections JSON 内保留键：记录仍标记「AI 起草」的段落名列表（字段级来源标记，C29/M9）。
//
// 设计取舍：Postmortem schema 仅有复盘级 generated_by 枚举（有 LLM 恒 mixed），无段级来源字段。
// 为支持「每段可标记 AI 草拟 vs 人工、编辑后清除该段 AI 标记」而不引入 schema 迁移，把段级标记
// 内联进 sections JSON 的保留键 `_ai_sections`（值为 []string 段名）。GenerateDraft 时把 AI 起草的
// 段落写入此列表；逐段编辑某段时从列表移除该段（人工已确认）。前端据此对每段渲染「AI 草拟」徽标。
// 下划线前缀表明它是元数据而非业务章节，前端/extractPostmortemText 均应跳过。
const aiSectionsKey = "_ai_sections"

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

// IncidentCloser 复盘发布联动关闭 incident 的接口（由 incident.Service.Close 适配注入）。
//
// 为什么用接口而非直接依赖 incident.Service：postmortem 不反向依赖 incident 包，
// 避免构建期依赖环——与 runbook→incident 升级触发同款解耦（见 wire.go runbookEscalator）。
// nil 时复盘发布不联动关闭 incident（降级：单据仍停 resolved，需人工 close）。
type IncidentCloser interface {
	// Close 把 incID 推进到 closed 终态。actorID 为发起人（0=系统）。
	// 幂等语义由实现方保证：已 closed / 非 resolved 时不应阻断复盘发布（best-effort）。
	Close(ctx context.Context, incID int, actorID int) error
}

// TicketCreator 复盘发布联动建外部工单的接口（由 ticket.Engine 适配注入，T4.3）。
//
// 为什么用接口而非直接依赖 ticket 包：与 IncidentCloser 同理，postmortem 不反向依赖具体
// 集成实现，避免构建期依赖环，也让工单集成可插拔（未注入时复盘发布不建单）。
// nil 时复盘发布不联动建单（降级：ActionItem 的 tracker_url 需人工填）。
type TicketCreator interface {
	// OnPostmortempublished 复盘发布时为该复盘下未建单的 ActionItem 建外部工单，回写 tracker_url。
	// best-effort：实现方须保证工单系统不可达/失败不阻断复盘发布（内部吞错，不返回）。
	OnPostmortemPublished(ctx context.Context, pmID int)
}

// Engine 复盘引擎。
type Engine struct {
	db       *ent.Client
	llm      LLMProvider    // 可为 nil（无 AI 时降级）
	embedder Embedder       // 可为 nil（无 embedding 时 published 不入库检索）
	closer   IncidentCloser // 可为 nil（无联动时 published 不自动关闭 incident）
	ticket   TicketCreator  // 可为 nil（无联动时 published 不自动建工单）
	// autoDraftWarning 控制 warning 级事件 resolved 是否自动起草复盘（T4.1）。
	//
	// 触发档位（docs/capabilities/08-postmortem.md §3）：
	//   - critical：强制自动起草（无条件建 draft）——不受此开关影响。
	//   - warning ：可配——默认 false（建议但不强制），置 true 则自动起草。
	//   - info    ：不强制，不自动起草。
	//
	// 设计简化说明：文档中 warning 档为「team 级可配」，但当前无 team 级复盘配置载体
	// （Team schema 无复盘策略字段）。为不引入过度设计，本轮以「全局默认」实现——
	// 由 main 装配经环境变量（VIGIL_POSTMORTEM_AUTO_DRAFT_WARNING）设置。
	// 后续若需 team 级差异化，可在此接入 Team 配置查询而不改调用方。
	autoDraftWarning bool
}

// NewEngine 创建复盘引擎。llm 可为 nil。
func NewEngine(db *ent.Client, llm LLMProvider) *Engine {
	return &Engine{db: db, llm: llm}
}

// SetAutoDraftWarning 配置 warning 级事件是否自动起草复盘（T4.1，main 装配时调用）。
func (e *Engine) SetAutoDraftWarning(v bool) { e.autoDraftWarning = v }

// SetEmbedder 注入向量化器（main 装配时调用）。
// 配置后 published 复盘计算 embedding 入库，供知识沉淀检索（M12.6）。
func (e *Engine) SetEmbedder(em Embedder) { e.embedder = em }

// SetIncidentCloser 注入 incident 关闭器（main 装配时调用）。
// 配置后复盘发布（→published）联动把关联 incident 从 resolved 推进到 closed 终态，
// 实现「复盘完成 → 单据收口」闭环。未注入时降级为不联动（单据停 resolved）。
func (e *Engine) SetIncidentCloser(c IncidentCloser) { e.closer = c }

// SetTicketCreator 注入工单集成建单器（main 装配时调用，T4.3）。
// 配置后复盘发布（→published）联动为其下未建单的 ActionItem 建外部工单并回写 tracker_url。
// best-effort：工单系统不可达/失败不阻断复盘发布。未注入时降级为不建单。
func (e *Engine) SetTicketCreator(t TicketCreator) { e.ticket = t }

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

	// aiSections 收集本次「实际由 AI 起草成功」的段落名（字段级来源标记，C29/M9）。
	// 仅 LLM 真产出内容的段落算 AI 草拟；降级到 fallback/占位的段落算人工待填，不打 AI 标记。
	var aiSections []string
	sections["summary"], aiSections = e.draftMark(ctx, "summary", ctxMap, fallbackSummary(inc), aiSections)
	sections["impact"], aiSections = e.draftMark(ctx, "impact", ctxMap, fallbackImpact(inc), aiSections)
	sections["root_cause"], aiSections = e.draftMark(ctx, "root_cause", ctxMap, "（待人工填写）", aiSections)
	sections["what_went_well"] = []string{"（待人工补充）"}
	sections["what_went_wrong"] = []string{"（待人工补充）"}
	sections["action_items"] = []any{}
	if len(aiSections) > 0 {
		sections[aiSectionsKey] = aiSections
	}

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
		// S7 覆盖保护：仅 draft 允许重新起草（幂等重跑）。
		// 已 in_review/published/archived 的复盘 sections 是人工校对/发布过的成果，
		// 无条件 SetSections 会把它冲掉——拒绝覆盖，交 handler 返回 409。
		if postmortem.Status(existing.Status) != postmortem.StatusDraft {
			return nil, ErrPostmortemNotDraft
		}
		// draft：更新草稿
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

// OnIncidentResolved 订阅 IncidentResolved 领域事件，按 severity 决定是否自动起草复盘（T4.1）。
//
// 触发档位（docs/capabilities/08-postmortem.md §3）：
//   - critical：强制自动起草（建 draft 复盘）。
//   - warning ：可配（autoDraftWarning，默认不强制）。
//   - info    ：不强制，不起草。
//
// 复用 GenerateDraft（幂等：已有 draft 更新，已发布/评审中的复盘被 S7 覆盖保护拒绝——
// 此处静默容忍，自动起草不应回冲人工成果）。best-effort：起草失败仅记日志，不阻断事件派发
// （与事件总线 best-effort 扇出契约一致）。
//
// 装配：bus.Subscribe(event.IncidentResolved, engine.OnIncidentResolved)。
func (e *Engine) OnIncidentResolved(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	if !e.shouldAutoDraft(incident.Severity(ev.Incident.Severity)) {
		return nil
	}
	_, err := e.GenerateDraft(ctx, ev.Incident.ID)
	if err != nil {
		// S7 覆盖保护：已脱离 draft 的复盘不重复起草——自动起草场景视为正常（人工已接管），不上报为错误。
		if errors.Is(err, ErrPostmortemNotDraft) {
			return nil
		}
		slog.Warn("postmortem auto-draft on resolve failed",
			"incident_id", ev.Incident.ID, "severity", ev.Incident.Severity, "error", err)
		return err
	}
	return nil
}

// shouldAutoDraft 按 severity 判定是否自动起草复盘（T4.1 触发档位）。
func (e *Engine) shouldAutoDraft(sev incident.Severity) bool {
	switch sev {
	case incident.SeverityCritical:
		return true // 强制
	case incident.SeverityWarning:
		return e.autoDraftWarning // 可配，默认 false
	default:
		return false // info 及其它不强制
	}
}

// HasPublishedPostmortem 实现 incident.PostmortemGate（T4.1 复盘闸门查询）。
//
// 判定「该 incident 是否已完成复盘」——存在 published 或 archived 复盘即视为已完成
// （archived 是 published 之后的归档态，同样代表复盘走完，不该再被闸门拦）。
// draft/in_review 不算完成（复盘尚未定稿发布，闸门仍应拦 close）。
func (e *Engine) HasPublishedPostmortem(ctx context.Context, incID int) (bool, error) {
	return e.db.Postmortem.Query().
		Where(
			postmortem.HasIncidentWith(incident.IDEQ(incID)),
			postmortem.StatusIn(postmortem.StatusPublished, postmortem.StatusArchived),
		).
		Exist(ctx)
}

// draftMark 用 AI 起草并返回段级来源标记：AI 真产出内容时把 section 名追加进
// aiSections（字段级「AI 草拟」标记）；降级到 fallback 则不追加（视为人工待填）。
func (e *Engine) draftMark(ctx context.Context, section string, ctxMap map[string]any, fallback string, aiSections []string) (string, []string) {
	if e.llm == nil {
		return fallback, aiSections
	}
	draft, err := e.llm.DraftSection(ctx, section, ctxMap)
	if err != nil || strings.TrimSpace(draft) == "" {
		return fallback, aiSections
	}
	return draft, append(aiSections, section)
}

// EditSections 逐段编辑复盘 sections（T4.2，C29/M9）。
//
// 语义：**部分更新**——patch 里出现的段落覆盖原值，未出现的段落原样保留（非整体替换）。
// 编辑过的段落从 `_ai_sections` 移除其 AI 草拟标记（人工已确认/改写该段，来源转为人工）。
//
// 状态门禁：仅 draft/in_review 可编辑（评审进行时）；published/archived 已定稿锁定，返 ErrPostmortemLocked。
// 权限由 handler 侧 postmortem.update 校验，本方法只管领域逻辑。
//
// patch 里的保留键（_ai_sections 等下划线前缀）被忽略，不允许经编辑接口直接改标记。
func (e *Engine) EditSections(ctx context.Context, pmID int, patch map[string]any) (*ent.Postmortem, error) {
	pm, err := e.db.Postmortem.Get(ctx, pmID)
	if err != nil {
		return nil, fmt.Errorf("get postmortem: %w", err)
	}
	// 定稿锁定：published/archived 不许原地改段（见 ErrPostmortemLocked 注释）。
	if s := postmortem.Status(pm.Status); s == postmortem.StatusPublished || s == postmortem.StatusArchived {
		return nil, ErrPostmortemLocked
	}

	// 过滤保留键：编辑接口只改业务段落，不许直接写 _ai_sections 之类元数据。
	edited := make([]string, 0, len(patch))
	for k := range patch {
		if strings.HasPrefix(k, "_") {
			continue
		}
		edited = append(edited, k)
	}
	if len(edited) == 0 {
		return nil, ErrNoSections
	}

	// 部分合并：复制原 sections，逐段覆盖 patch 提供的段落，其余保留。
	sections := cloneSections(pm.Sections)
	for _, k := range edited {
		sections[k] = patch[k]
	}
	// 清除被编辑段落的 AI 草拟标记（人工确认过，来源转人工）。
	sections[aiSectionsKey] = removeAISections(sections[aiSectionsKey], edited)
	if len(sections[aiSectionsKey].([]string)) == 0 {
		delete(sections, aiSectionsKey)
	}

	updated, err := e.db.Postmortem.UpdateOneID(pmID).SetSections(sections).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update sections: %w", err)
	}
	return updated, nil
}

// cloneSections 浅拷贝 sections map（避免直接改动 ent 实体持有的 map）。
func cloneSections(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// removeAISections 从 AI 段标记列表中移除已被人工编辑的段落，返回剩余标记（[]string）。
//
// 兼容读取：sections 经 JSON 往返后 `_ai_sections` 可能是 []any（元素 string），也可能是
// 内存态 []string；两种都归一处理。返回值恒为 []string，供 SetSections 序列化。
func removeAISections(raw any, edited []string) []string {
	editedSet := make(map[string]struct{}, len(edited))
	for _, e := range edited {
		editedSet[e] = struct{}{}
	}
	var cur []string
	switch v := raw.(type) {
	case []string:
		cur = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				cur = append(cur, s)
			}
		}
	}
	out := make([]string, 0, len(cur))
	for _, s := range cur {
		if _, hit := editedSet[s]; !hit {
			out = append(out, s)
		}
	}
	return out
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
	// embedder 未配置或计算失败不阻塞 publish（降级：复盘仍发布，但暂不进检索库）。
	//
	// B18 补算：计算失败不再完全静默——记 Warn 供运维排查，且失败留下的空 embedding
	// 会在后续相似检索时被 ai 侧 backfillPostmortemEmbeddings 懒补算（检索库最终一致），
	// 不会永久掉出向量检索。archived 复盘同样纳入检索（见 ai 侧 knowledgePostmortemStatuses）。
	if target == postmortem.StatusPublished && e.embedder != nil {
		if eerr := e.ensurePublishedEmbedding(ctx, pm); eerr != nil {
			slog.Warn("postmortem publish: embedding compute failed, will be backfilled on similarity retrieval",
				"postmortem_id", pm.ID, "error", eerr)
		}
	}
	// 单据收口（closed 终态联动）：复盘发布 → 关联 incident 从 resolved 推进到 closed。
	// best-effort：关闭失败/已 closed/不在 resolved 都不阻断复盘发布（closeLinkedIncident 内部容错）。
	if target == postmortem.StatusPublished && e.closer != nil {
		e.closeLinkedIncident(ctx, pm)
	}
	// 工单联动（T4.3）：复盘发布 → 为其下 ActionItem 建外部工单，回写 tracker_url。
	// best-effort：工单系统不可达/失败不阻断复盘发布（TicketCreator 内部吞错，不返回）。
	if target == postmortem.StatusPublished && e.ticket != nil {
		e.ticket.OnPostmortemPublished(ctx, pm.ID)
	}
	return pm, nil
}

// closeLinkedIncident 复盘发布时把关联 incident 推进到 closed 终态（best-effort）。
//
// 容错要点（不阻断复盘发布）：
//   - 查不到关联 incident：跳过（复盘可能无 incident 边，理论上不该发生但防御）。
//   - incident 不在 resolved（如已 closed、或被 reopen 回 triggered）：Close 内部按状态机处理，
//     已 closed 返回 ErrAlreadyClosed（幂等无操作），其它非法转换仅记日志不上抛。
//
// 联动是「复盘完成 → 收口」的锦上添花，绝不能因关闭失败让复盘卡在无法发布。
func (e *Engine) closeLinkedIncident(ctx context.Context, pm *ent.Postmortem) {
	inc, err := pm.QueryIncident().Only(ctx)
	if err != nil {
		// 无关联 incident 或查询失败：不阻断，仅当无联动。
		return
	}
	// actorID=0：复盘发布联动属系统触发，time line/事件 actor 记为系统。
	if cerr := e.closer.Close(ctx, inc.ID, 0); cerr != nil {
		// ErrAlreadyClosed（幂等）与其它非法转换均视为「无需/无法收口」，best-effort 忽略。
		// closer 实现（wire.go incidentCloser）已把 ErrAlreadyClosed 吞掉返回 nil，
		// 走到这里的多是「incident 不在 resolved」等场景，静默即可（复盘已发布成功）。
		_ = cerr
	}
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
