// engine.go 工单集成引擎（T4.3）：为 ActionItem 建外部工单并回写 tracker_url。
package ticket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/actionitem"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/ticketintegration"
	"github.com/kevin/vigil/internal/crypto"
)

// Engine 工单集成引擎：解析 ActionItem 适用的工单集成 → 调适配器建单 → 回写 tracker_url。
//
// 全程 best-effort（见包注释）：任何一步失败仅记日志，不上抛阻断调用方（复盘发布不受影响）。
type Engine struct {
	db       *ent.Client
	adapters map[string]Adapter // type → 适配器（webhook 已实现，jira/zentao 预留）
	// cipher 凭据加密器（T6.3 统一加密机制，可选）。非 nil 时把 integ.Credential 视为密文解密后
	// 再传给适配器；nil 或解密失败时按明文透传（向后兼容 T4.3 既有明文凭据）。
	cipher *crypto.Cipher
}

// NewEngine 创建工单引擎。adapters 为 type→适配器映射；缺失类型建单时降级（记日志不建）。
func NewEngine(db *ent.Client, adapters ...Adapter) *Engine {
	m := make(map[string]Adapter, len(adapters))
	for _, a := range adapters {
		if a != nil {
			m[a.Type()] = a
		}
	}
	return &Engine{db: db, adapters: m}
}

// SetCipher 注入凭据加密器（T6.3）：让工单凭据也复用统一 AES-256-GCM 机制加密存储。
// 装配层与 Runbook 执行器共用同一 cipher（同源密钥），避免两套加密。
func (e *Engine) SetCipher(c *crypto.Cipher) { e.cipher = c }

// resolveCredential 把存储的凭据解成明文（仅内存传给适配器）。
// cipher 非 nil 且能解密 → 返回明文；否则（无 cipher / 解密失败）按明文透传，
// 兼容 T4.3 迁移前落库的明文凭据（新写入的经 handler 加密，旧数据仍可用）。
func (e *Engine) resolveCredential(stored string) string {
	if stored == "" || e.cipher == nil {
		return stored
	}
	if plain, err := e.cipher.Decrypt(stored); err == nil {
		return plain
	}
	return stored // 解密失败：视为历史明文（不打印，不报错）
}

// OnPostmortemPublished 复盘发布联动：为该复盘下未建单的 ActionItem 建外部工单，回写 tracker_url。
//
// 触发点：postmortem.Engine 发布（→published）时经 TicketCreator 接口调用（best-effort）。
// 幂等：只处理 tracker_url 为空的 ActionItem（已有工单的跳过，避免重复建单）。
// 无适用集成（team/org 均未配 enabled 工单集成）时直接返回（不建单，不报错）。
func (e *Engine) OnPostmortemPublished(ctx context.Context, pmID int) {
	items, err := e.db.ActionItem.Query().
		Where(
			actionitem.HasPostmortemWith(postmortem.IDEQ(pmID)),
			// 仅未建单的：tracker_url 为空。Optional 字段未设时 DB 存 NULL（SQL `NULL=''` 为假），
			// 故须同时匹配 IsNil 与空串。
			actionitem.Or(actionitem.TrackerURLIsNil(), actionitem.TrackerURLEQ("")),
		).
		All(ctx)
	if err != nil {
		slog.Warn("ticket: query action items for postmortem failed", "postmortem_id", pmID, "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	// 解析该复盘归属 team（经 postmortem→incident→team）；无 team 视为 org 级。
	teamID := e.resolveTeamID(ctx, pmID)
	integ, err := e.resolveIntegration(ctx, teamID)
	if err != nil || integ == nil {
		// 无适用集成：静默（未配置工单集成是常态，不是错误）。
		return
	}
	for _, ai := range items {
		e.createForActionItem(ctx, integ, ai, pmID)
	}
}

// CreateForActionItem 为单个 ActionItem 建工单并回写 tracker_url（best-effort）。
// 供 ActionItem 创建时（可选）即时建单，或 OnPostmortemPublished 批量调用。
// 返回是否成功回写了 tracker_url（供调用方/测试判定）。
func (e *Engine) CreateForActionItem(ctx context.Context, ai *ent.ActionItem) bool {
	if ai.TrackerURL != "" {
		return false // 已有工单，不重复建
	}
	pmID := e.postmortemID(ctx, ai)
	teamID := e.resolveTeamID(ctx, pmID)
	integ, err := e.resolveIntegration(ctx, teamID)
	if err != nil || integ == nil {
		return false
	}
	return e.createForActionItem(ctx, integ, ai, pmID)
}

// createForActionItem 内部建单实现：调适配器 → 回写 tracker_url。返回是否回写成功。
func (e *Engine) createForActionItem(ctx context.Context, integ *ent.TicketIntegration, ai *ent.ActionItem, pmID int) bool {
	adapter := e.adapters[integ.Type.String()]
	if adapter == nil {
		slog.Warn("ticket: no adapter for integration type, skip create",
			"type", integ.Type, "action_item_id", ai.ID)
		return false
	}
	req := buildTicketRequest(ai, pmID)
	cfg := AdapterConfig{
		Endpoint:   integ.Endpoint,
		Credential: e.resolveCredential(integ.Credential), // 解密后明文仅内存传给适配器，不落日志
		Config:     integ.Config,
	}
	res, err := adapter.CreateTicket(ctx, cfg, req)
	if err != nil {
		// best-effort：建单失败（不可达/未实现/超时）不阻断，记日志留待人工/重试。
		lvl := "ticket: create ticket failed (best-effort, postmortem publish not blocked)"
		if errors.Is(err, ErrAdapterNotImplemented) {
			lvl = "ticket: adapter not implemented, skipping create (reserved for jira/zentao)"
		}
		slog.Warn(lvl, "type", integ.Type, "action_item_id", ai.ID, "error", err)
		return false
	}
	if res == nil || strings.TrimSpace(res.TrackerURL) == "" {
		// 建单成功但未拿到 URL：未闭环，记日志（不回写空 URL）。
		slog.Warn("ticket: created but no tracker_url returned, not backfilling",
			"type", integ.Type, "action_item_id", ai.ID)
		return false
	}
	upd := e.db.ActionItem.UpdateOneID(ai.ID).SetTrackerURL(res.TrackerURL)
	// N1.3：一并回写 external_id（若适配器返回）——工单侧状态回调据此精确匹配本 ActionItem。
	if xid := strings.TrimSpace(res.ExternalID); xid != "" {
		upd = upd.SetExternalID(xid)
	}
	if err := upd.Exec(ctx); err != nil {
		slog.Warn("ticket: backfill tracker_url failed", "action_item_id", ai.ID, "error", err)
		return false
	}
	slog.Info("ticket: created and backfilled tracker_url",
		"action_item_id", ai.ID, "tracker_url", res.TrackerURL, "external_id", res.ExternalID)
	return true
}

// SyncStatus 单向同步 ActionItem 状态到外部工单（T4.3 单向回写，可选）。
//
// 当前实现边界（不夸大）：ActionItem 状态变为 done 时，若配了工单集成且 webhook 适配器支持，
// 向 endpoint 发一条状态同步（复用 CreateTicket 的 webhook 通道语义，payload 标 status=done）。
// **单向**：只从 Vigil 推到工单侧，不反向拉工单状态回 Vigil（工单侧回写是设计目标，非本轮实现）。
// best-effort：同步失败不阻断状态变更。返回是否成功推送。
//
// 说明：当前仅通用 webhook 适配器承载状态同步（Jira/禅道预留时同步也降级为 no-op）。
func (e *Engine) SyncStatus(ctx context.Context, ai *ent.ActionItem) bool {
	if ai.Status != actionitem.StatusDone {
		return false // 仅 done 触发单向同步
	}
	if ai.TrackerURL == "" {
		return false // 无外部工单可同步
	}
	pmID := e.postmortemID(ctx, ai)
	teamID := e.resolveTeamID(ctx, pmID)
	integ, err := e.resolveIntegration(ctx, teamID)
	if err != nil || integ == nil {
		return false
	}
	adapter := e.adapters[integ.Type.String()]
	wa, ok := adapter.(*WebhookAdapter)
	if !ok {
		// 仅通用 webhook 承载状态同步；Jira/禅道预留时 no-op（不阻断）。
		return false
	}
	req := buildTicketRequest(ai, pmID)
	req.Title = "[status] " + req.Title
	cfg := AdapterConfig{Endpoint: integ.Endpoint, Credential: e.resolveCredential(integ.Credential), Config: integ.Config}
	// 复用 webhook 通道推状态：payload 带 status=done 语义由 description 标注。
	req.Description = fmt.Sprintf("ActionItem #%d 状态更新为 done。%s", ai.ID, req.Description)
	if _, serr := wa.CreateTicket(ctx, cfg, req); serr != nil {
		slog.Warn("ticket: sync status failed (best-effort)", "action_item_id", ai.ID, "error", serr)
		return false
	}
	return true
}

// CallbackStatus 工单侧回调携带的外部工单状态（归一化后）。
type CallbackStatus string

const (
	// CallbackStatusOpen 工单新建/待处理 → ActionItem open。
	CallbackStatusOpen CallbackStatus = "open"
	// CallbackStatusInProgress 工单处理中 → ActionItem in_progress。
	CallbackStatusInProgress CallbackStatus = "in_progress"
	// CallbackStatusDone 工单关闭/完成 → ActionItem done。
	CallbackStatusDone CallbackStatus = "done"
)

// CallbackResult 一次回调处理结果（供 handler 组织响应/日志，不含敏感信息）。
type CallbackResult struct {
	// Matched 是否匹配到 ActionItem（未匹配时其余字段无意义）。
	Matched bool
	// ActionItemID 命中的 ActionItem id（Matched 时有值）。
	ActionItemID int
	// Changed 本次是否真正改变了状态（幂等：重复回调同状态时为 false）。
	Changed bool
	// FromStatus / ToStatus 状态迁移（供审计/日志）。
	FromStatus string
	ToStatus   string
}

// normalizeCallbackStatus 把外部工单系统五花八门的状态字符串归一到 ActionItem 三态。
// 无法归一（未知状态）返回 ("", false)——调用方据此忽略该回调（不误改状态）。
//
// 归一规则（宽松匹配常见工单状态词，大小写不敏感）：
//   - done   ：closed/resolved/done/complete(d)/fixed/finished
//   - in_progress：in_progress/in progress/doing/started/wip/processing
//   - open   ：open/new/todo/reopened/backlog
func normalizeCallbackStatus(raw string) (CallbackStatus, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "closed", "close", "resolved", "resolve", "done", "complete", "completed", "fixed", "finished":
		return CallbackStatusDone, true
	case "in_progress", "inprogress", "doing", "started", "start", "wip", "processing":
		return CallbackStatusInProgress, true
	case "open", "new", "todo", "reopened", "reopen", "backlog", "pending":
		return CallbackStatusOpen, true
	default:
		return "", false // 未知状态：忽略（不猜测，避免误改）
	}
}

// actionItemStatus 把归一后的回调状态映射到 ent ActionItem 枚举。
func (s CallbackStatus) actionItemStatus() actionitem.Status {
	switch s {
	case CallbackStatusDone:
		return actionitem.StatusDone
	case CallbackStatusInProgress:
		return actionitem.StatusInProgress
	default:
		return actionitem.StatusOpen
	}
}

// HandleCallback 处理一条工单侧状态变更回调（N1.3 双向回写）：
// 据 externalID / trackerURL 匹配对应 ActionItem，把其 status 更新为 status 归一后的值。
//
// 设计契约（与 T4.3 单向同步对称）：
//   - 幂等：目标 ActionItem 已是该状态 → Changed=false，不重复写、不重复留痕。
//   - best-effort：匹配不到（externalID/trackerURL 均无对应 ActionItem）→ Matched=false，
//     调用方据此返回 200 ignored（回调常来自与本系统无关的工单，忽略是常态，非错误）。
//   - 未知外部状态 → 归一失败，返回 (nil, ErrCallbackUnknownStatus)，handler 据此 400。
//
// 鉴权/验签由 handler 层完成（本方法只做匹配 + 落状态），保持引擎纯粹。
func (e *Engine) HandleCallback(ctx context.Context, externalID, trackerURL, rawStatus string) (*CallbackResult, error) {
	norm, ok := normalizeCallbackStatus(rawStatus)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrCallbackUnknownStatus, rawStatus)
	}
	ai, err := e.matchActionItem(ctx, externalID, trackerURL)
	if err != nil {
		return nil, err
	}
	if ai == nil {
		return &CallbackResult{Matched: false}, nil // 未匹配：忽略（非错误）
	}
	target := norm.actionItemStatus()
	res := &CallbackResult{
		Matched:      true,
		ActionItemID: ai.ID,
		FromStatus:   string(ai.Status),
		ToStatus:     string(target),
	}
	if ai.Status == target {
		return res, nil // 幂等：已是目标状态，不重复写
	}
	if err := e.db.ActionItem.UpdateOneID(ai.ID).SetStatus(target).Exec(ctx); err != nil {
		return nil, fmt.Errorf("update action item status: %w", err)
	}
	res.Changed = true
	slog.Info("ticket: callback updated action item status",
		"action_item_id", ai.ID, "from", res.FromStatus, "to", res.ToStatus)
	return res, nil
}

// matchActionItem 据 externalID（优先，精确）/ trackerURL（兜底，精确）匹配 ActionItem。
// externalID/trackerURL 均为空或都匹配不到时返回 (nil, nil)（未匹配，非错误）。
// external_id 是回调匹配主键（建单时回写），trackerURL 兜底既有仅存 URL 的历史数据。
func (e *Engine) matchActionItem(ctx context.Context, externalID, trackerURL string) (*ent.ActionItem, error) {
	externalID = strings.TrimSpace(externalID)
	trackerURL = strings.TrimSpace(trackerURL)
	if externalID != "" {
		ai, err := e.db.ActionItem.Query().
			Where(actionitem.ExternalIDEQ(externalID)).First(ctx)
		if err == nil {
			return ai, nil
		}
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("query action item by external_id: %w", err)
		}
		// external_id 无命中：继续尝试 tracker_url 兜底（下面）。
	}
	if trackerURL != "" {
		ai, err := e.db.ActionItem.Query().
			Where(actionitem.TrackerURLEQ(trackerURL)).First(ctx)
		if err == nil {
			return ai, nil
		}
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("query action item by tracker_url: %w", err)
		}
	}
	return nil, nil // 均未命中
}

// resolveIntegration 解析适用的工单集成：优先 team 级 enabled，其次 org 级（无 team 归属）。
// 返回 (nil, nil) 表示无适用集成（正常，非错误）。
func (e *Engine) resolveIntegration(ctx context.Context, teamID int) (*ent.TicketIntegration, error) {
	// 先查 team 级（若有 team 归属）。
	if teamID > 0 {
		integ, err := e.db.TicketIntegration.Query().
			Where(
				ticketintegration.EnabledEQ(true),
				ticketintegration.HasTeamWith(team.IDEQ(teamID)),
			).
			First(ctx)
		if err == nil && integ != nil {
			return integ, nil
		}
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
	}
	// 兜底 org 级（无 team 归属的集成，全组织可用）。
	integ, err := e.db.TicketIntegration.Query().
		Where(
			ticketintegration.EnabledEQ(true),
			ticketintegration.Not(ticketintegration.HasTeam()),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil // 无适用集成（非错误）
		}
		return nil, err
	}
	return integ, nil
}

// resolveTeamID 经 postmortem→incident→team 反查复盘归属 team id；无归属返回 0。
func (e *Engine) resolveTeamID(ctx context.Context, pmID int) int {
	if pmID <= 0 {
		return 0
	}
	pm, err := e.db.Postmortem.Query().
		Where(postmortem.IDEQ(pmID)).
		WithIncident(func(q *ent.IncidentQuery) { q.WithTeam() }).
		Only(ctx)
	if err != nil || pm.Edges.Incident == nil || pm.Edges.Incident.Edges.Team == nil {
		return 0
	}
	return pm.Edges.Incident.Edges.Team.ID
}

// postmortemID 反查 ActionItem 所属复盘 id；无归属返回 0。
func (e *Engine) postmortemID(ctx context.Context, ai *ent.ActionItem) int {
	pm, err := ai.QueryPostmortem().Only(ctx)
	if err != nil {
		return 0
	}
	return pm.ID
}

// buildTicketRequest 把 ActionItem 组装成中立建单请求。
func buildTicketRequest(ai *ent.ActionItem, pmID int) TicketRequest {
	title := ai.Description
	if len(title) > 120 {
		title = title[:120]
	}
	req := TicketRequest{
		Title:        title,
		Description:  ai.Description,
		OwnerID:      ai.OwnerID,
		ActionItemID: ai.ID,
		PostmortemID: pmID,
	}
	if ai.DueDate != nil {
		req.DueDate = ai.DueDate.Format("2006-01-02T15:04:05Z07:00")
	}
	return req
}
