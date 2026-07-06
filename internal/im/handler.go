// handler.go IM Webhook 回调接收与操作执行（能力域 8 §6 鉴权链路 + §8 状态同步）。
//
// 回调处理流程（capabilities §6）：
//
//	IM Webhook 回调（im_platform + im_unionid + action）
//	  → bot.VerifyCallback（签名/解密）
//	  → bot.ParseCallback（标准化为 IMEvent）
//	  → mapper.ResolveUser（im_unionid → User，未绑定拒绝）
//	  → action → 权限点（ack → incident.ack ...）
//	  → authz.Check（带 incident.team scope）
//	  → incident.Service.Ack/Resolve/... （核心服务执行）
//	  → bot.UpdateCard（刷新已发卡片）
//	  → 时间线 source=im（已在 incident.Service 记录）
package im

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	entservice "github.com/kevin/vigil/ent/service"
	entteam "github.com/kevin/vigil/ent/team"
	entuser "github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"
	imincident "github.com/kevin/vigil/internal/incident"

	"github.com/labstack/echo/v5"
)

// CardStore 抽象与实现见 cardstore.go（内存 / Redis 持久化，B24）。

// Handler IM Webhook 回调与卡片操作链路。
type Handler struct {
	db       *ent.Client
	registry *Registry
	mapper   *Mapper
	authz    *auth.Authorizer
	incSvc   *imincident.Service
	renderer *Renderer
	cards    CardStore
	audit    *auth.AuditRecorder // IM 越权拒绝留痕（S9，可选注入，nil 时跳过）
	// runbooks / schedules 为 M8.5 斜杠命令 /vigil runbook / /vigil oncall 依赖的引擎（可选注入）。
	// 解耦：用接口而非直接持 runbook.Engine / schedule.Engine，避免 im→runbook/schedule 反向耦合，
	// 且便于测试注入桩。nil 时对应命令回「命令未启用」。
	runbooks  RunbookTrigger
	schedules OncallResolver
}

// RunbookTrigger 抽象 Runbook 执行引擎（/vigil runbook 命令用）。
// 由装配层用 runbook.Engine 适配注入。approved=false 时写步骤被审批闸门阻断（不在 IM 内绕过）。
type RunbookTrigger interface {
	// Execute 执行指定 Runbook 对该 incident；approved 恒 false（IM 内绝不代替人工审批）。
	// 返回结构应至少能判定是否有写步骤被阻断（PendingApproval）与每步成败摘要。
	Execute(ctx context.Context, runbookID, incID int, approved bool, actorID int) (*RunbookRunResult, error)
	// LookupByName 按名解析 Runbook（团队软隔离由调用方用 team scope 校验权限保证）。
	LookupByName(ctx context.Context, name string) (runbookID, teamID int, err error)
}

// RunbookRunResult IM 侧需要的 Runbook 执行结果摘要（与 runbook.ExecuteResult 字段对齐子集）。
type RunbookRunResult struct {
	PendingApproval bool     // 是否存在因未获审批被阻断的写步骤（human-in-the-loop 闸门生效）
	Aborted         bool     // 是否中止
	Reason          string   // 中止原因
	StepSummaries   []string // 每步一行摘要（名称 + 成败/跳过）
}

// OncallResolver 抽象排班引擎的「查当前值班」（/vigil oncall 命令用）。
// 由装配层用 schedule.Engine + team/service 解析适配注入。
type OncallResolver interface {
	// OncallForTeam 查某 team 全部 Schedule 的当前值班摘要（每行一条）。
	OncallForTeam(ctx context.Context, teamID int) ([]string, error)
	// OncallForTeamName / OncallForServiceName 按名解析后查值班。名不存在返回 error。
	OncallForTeamName(ctx context.Context, name string) ([]string, error)
	OncallForServiceName(ctx context.Context, name string) ([]string, error)
}

// NewHandler 创建 IM handler。
// hasPermission 为权限判定回调（通常包装 auth.Authorizer.CheckAny）。
// audit 记录 IM 越权拒绝（S9）：IM 是主交互面，越权探测必须与 Web 一样留痕；nil 时降级不记。
func NewHandler(
	db *ent.Client,
	registry *Registry,
	mapper *Mapper,
	authz *auth.Authorizer,
	incSvc *imincident.Service,
	renderer *Renderer,
	cards CardStore,
	audit *auth.AuditRecorder,
) *Handler {
	return &Handler{
		db: db, registry: registry, mapper: mapper, authz: authz,
		incSvc: incSvc, renderer: renderer, cards: cards, audit: audit,
	}
}

// SetRunbookTrigger 注入 Runbook 执行引擎（启用 /vigil runbook 命令）。nil 时该命令回「未启用」。
func (h *Handler) SetRunbookTrigger(r RunbookTrigger) { h.runbooks = r }

// SetOncallResolver 注入排班值班解析器（启用 /vigil oncall 命令）。nil 时该命令回「未启用」。
func (h *Handler) SetOncallResolver(o OncallResolver) { h.schedules = o }

// Register 挂载 IM 回调路由。
//
//	POST /api/v1/im/:platform/callback
//
// 各平台回调入口，platform 为 feishu/dingtalk/wecom。
func (h *Handler) Register(g *echo.Group) {
	g.POST("/im/:platform/callback", h.callback)
}

// RegisterStatus 挂载 IM 平台状态查询路由（业务只读，挂到鉴权 group）。
//
//	GET /api/v1/im/platforms
//
// 返回各 IM 平台适配器是否就绪（凭证已配置）。凭证敏感，仅返回 available 布尔，不回显。
func (h *Handler) RegisterStatus(g *echo.Group) {
	g.GET("/im/platforms", h.platforms)
}

// imPlatformStatus 单个平台状态。
type imPlatformStatus struct {
	Platform  string `json:"platform"`  // feishu | dingtalk | wecom
	Available bool   `json:"available"` // 凭证已配置且客户端就绪
	Impl      string `json:"impl"`      // 适配器类型：real | noop（占位）
}

// platforms 返回所有已注册 IM 平台的可用性。
//
// @Summary      IM 平台状态
// @Description  返回各 IM 平台适配器是否就绪（凭证已配置）。凭证敏感，不回显。
// @Tags         im
// @Produce      json
// @Success      200  {array}   im.imPlatformStatus
// @Security     bearerAuth
// @Router       /im/platforms [get]
func (h *Handler) platforms(c *echo.Context) error {
	if h.registry == nil {
		return c.JSON(http.StatusOK, []imPlatformStatus{})
	}
	out := make([]imPlatformStatus, 0, 3)
	for _, b := range h.registry.All() {
		impl := "real"
		if _, ok := b.(*NoopBot); ok {
			impl = "noop"
		}
		out = append(out, imPlatformStatus{
			Platform:  b.Platform(),
			Available: b.Available(),
			Impl:      impl,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// callback 统一回调入口。
//
// @Summary      IM 平台回调
// @Description  各平台（feishu/dingtalk/wecom）回调入口：签名校验 → 解析为标准事件 → 派发卡片/命令/@ 处理。公开入口，平台签名鉴权，不走 RBAC。
// @Tags         im
// @Accept       json
// @Produce      json
// @Param        platform  path    string  true  "IM 平台（feishu/dingtalk/wecom）"  Enums(feishu, dingtalk, wecom)
// @Success      200       {object} map[string]string
// @Failure      400       {object} httputil.ErrorResponse
// @Failure      401       {object} httputil.ErrorResponse
// @Failure      403       {object} httputil.ErrorResponse
// @Failure      404       {object} httputil.ErrorResponse
// @Router       /im/{platform}/callback [post]
func (h *Handler) callback(c *echo.Context) error {
	platform := c.Param("platform")
	bot, ok := h.registry.Get(platform)
	if !ok {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "unknown platform"})
	}

	// 1. 读取原始 body（校验/解密需要）
	rawBody, err := readBody(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "read body"})
	}
	headers := headerMap(c.Request().Header)

	// 2. 校验 + 解密
	payload, err := bot.VerifyCallback(headers, rawBody)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "verify failed"})
	}

	// 3. 解析为标准化事件
	evt, err := bot.ParseCallback(payload)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "parse failed"})
	}

	// 飞书 v2 事件订阅有 challenge 握手（URL 验证），直接回显
	if isChallenge(payload) {
		return c.JSON(http.StatusOK, echoChallenge(payload))
	}

	// 4. 派发处理
	ctx := c.Request().Context()
	switch evt.Type {
	case EventCardAction:
		return h.handleCardAction(c, ctx, bot, evt)
	case EventCommand:
		return h.handleCommand(c, ctx, bot, evt)
	case EventMention:
		return h.handleMention(c, ctx, bot, evt)
	default:
		// 普通消息：本期不回写时间线（开放问题 Q2，留后续）
		return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
	}
}

// handleCardAction 卡片按钮点击：鉴权 → 执行动作 → 刷新卡片。
func (h *Handler) handleCardAction(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	user, err := h.resolveAndCheck(c, ctx, evt, evt.Action)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	incID, _ := strconv.Atoi(evt.IncidentID)
	if incID == 0 {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid incident_id"})
	}

	var inc *ent.Incident
	switch evt.Action {
	case ActionAck:
		inc, err = h.incSvc.Ack(ctx, incID, user.ID, imincident.SourceIM)
	case ActionEscalate:
		inc, err = h.incSvc.Escalate(ctx, incID, user.ID, imincident.SourceIM)
	case ActionResolve:
		inc, err = h.incSvc.Resolve(ctx, incID, user.ID, imincident.SourceIM)
	case ActionDetail:
		// 详情按钮：回一个 Web 链接，不做状态变更
		return c.JSON(http.StatusOK, map[string]string{"detail_url": fmt.Sprintf("/incidents/%d", incID)})
	default:
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "unknown action"})
	}
	if err != nil {
		// B25 归一：IM 走与 Web 相同的错误码——不存在的 incident → 404，
		// 状态非法（ErrInvalidTransition，如对已 resolved 单再 ack）→ 400 failed_precondition，
		// 不再一律压成 500。
		return errs.FailActionState(c, nil, err, "incident")
	}

	// 刷新已发卡片（若有记录的 cardID）
	h.refreshCard(ctx, bot, inc, user)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "action": evt.Action})
}

// handleCommand 斜杠命令：/vigil ack INC-0042 等。
// 命令格式：<command> <incident_id|number> [args]
//
// runbook / oncall 命令参数形态与状态变更类命令不同（runbook 参数是 <name> <id>，
// oncall 可无参数），故先单独派发，再走通用「解析 incident → 鉴权 → 动作」路径。
func (h *Handler) handleCommand(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	switch evt.Command {
	case "runbook":
		return h.handleRunbookCommand(c, ctx, bot, evt)
	case "oncall":
		return h.handleOncallCommand(c, ctx, bot, evt)
	}

	cmd := evt.Command
	// 权限点按命令映射
	action := commandToAction(cmd)
	user, err := h.resolveAndCheck(c, ctx, evt, action)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	incID, err := h.resolveIncidentArg(ctx, evt.CommandArg)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}

	var inc *ent.Incident
	switch cmd {
	case "ack":
		inc, err = h.incSvc.Ack(ctx, incID, user.ID, imincident.SourceIM)
	case "escalate":
		inc, err = h.incSvc.Escalate(ctx, incID, user.ID, imincident.SourceIM)
	case "resolve":
		inc, err = h.incSvc.Resolve(ctx, incID, user.ID, imincident.SourceIM)
	case "status":
		// 查询命令：回卡片
		inc, _ = h.db.Incident.Get(ctx, incID)
		if inc != nil {
			h.sendCardToUser(ctx, bot, inc, evt.ChannelID, user)
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "sent"})
	default:
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "unsupported command: " + cmd})
	}
	if err != nil {
		// B25 归一：与 Web 一致——不存在 → 404，状态非法 → 400 failed_precondition（不再一律 500）。
		return errs.FailActionState(c, nil, err, "incident")
	}
	h.refreshCard(ctx, bot, inc, user)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "command": cmd})
}

// handleRunbookCommand /vigil runbook <runbook> <incident>：在 IM 内触发/展示 Runbook 执行。
//
// 安全铁律（capabilities §5 + 设计基线第 5/8 条）：
//   - 复用 runbook 执行引擎两档安全：只读诊断步骤自动执行；写步骤 require_approval。
//   - IM 内 approved 恒 false → 写步骤一律被 human-in-the-loop 闸门阻断（PendingApproval），
//     绝不在 IM 内代替人工审批放行写操作（写操作确认仍须回 Web/审批流）。
//   - 权限点 runbook.execute，team scope 取 incident 归属团队（与 Web 触发同一鉴权链）。
func (h *Handler) handleRunbookCommand(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	if h.runbooks == nil {
		return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{Error: "runbook command not enabled"})
	}
	// 参数解析：<runbook> <incident>（两段）。缺参回明确用法提示。
	rbName, incArg := splitTwo(evt.CommandArg)
	if rbName == "" || incArg == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "usage: /vigil runbook <runbook> <incident>"})
	}
	incID, err := h.resolveIncidentArg(ctx, incArg)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	// ★ 关键：先回填 IncidentID 供 resolveAndCheck 解 team scope，否则团队级 runbook.execute
	//   会被当 org 级判定，持团队权限者误拒（与 handleMention 同款回填）。
	evt.IncidentID = strconv.Itoa(incID)
	user, err := h.resolveAndCheck(c, ctx, evt, ActionRunbook)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	// 按名解析 runbook（找不到回明确错误，不泄露存在性以外的信息）。
	rbID, _, err := h.runbooks.LookupByName(ctx, rbName)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: fmt.Sprintf("runbook %q not found", rbName)})
	}
	// approved=false：IM 内绝不代替人工审批放行写操作。
	res, err := h.runbooks.Execute(ctx, rbID, incID, false, user.ID)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	// 回一张纯文本结果卡片（无按钮），把执行摘要 + 待审批提示呈现给触发者。
	h.sendRunbookResultCard(ctx, bot, evt.ChannelID, rbName, incID, res)
	return c.JSON(http.StatusOK, map[string]any{
		"status":           "ok",
		"command":          "runbook",
		"pending_approval": res.PendingApproval,
	})
}

// handleOncallCommand /vigil oncall [service|team]：查当前值班人。
//
// 无参数：查调用者所属团队的值班（多团队则汇总，只汇总有 schedule.view 权限的团队）。
// 有参数：按名解析为 service 或 team 后查（先试 team，再试 service），按目标团队判 schedule.view。
//
// ★ 鉴权铁律：team 级 schedule.view 绑定在 team scope=nil 时不生效（authz 只查 org 级），
// 故必须按目标团队显式判权限——先解析目标团队，再以该团队 scope 判 schedule.view，
// 避免持团队权限者被误判 org 级而拒（与 handleMention/handleRunbookCommand 回填 scope 同理）。
func (h *Handler) handleOncallCommand(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	if h.schedules == nil {
		return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{Error: "oncall command not enabled"})
	}
	arg := firstToken(strings.TrimSpace(evt.CommandArg))
	if arg == "" {
		return h.oncallForUserTeams(c, ctx, bot, evt)
	}
	return h.oncallForNamed(c, ctx, bot, evt, arg)
}

// oncallForUserTeams 处理无参 oncall：查调用者所属全部团队的值班（只含有 schedule.view 权限的团队）。
func (h *Handler) oncallForUserTeams(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	// 先解析 actor（每团队判权限需要 user.ID；未绑定同样落 denied 审计）。
	user, err := h.mapper.ResolveUser(ctx, evt.Platform, evt.UnionID)
	if err != nil {
		h.auditDenied(c, 0, evt, ActionOncall, "im account not bound")
		return replyErr(c, fmt.Errorf("im account not bound: %w", err), bot, evt.ChannelID)
	}
	teams, err := h.db.User.Query().Where(entuser.IDEQ(user.ID)).QueryTeams().All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if len(teams) == 0 {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "you are not a member of any team; use /vigil oncall <team|service>"})
	}
	perm := auth.Permission(PermissionMap[ActionOncall])
	var lines []string
	permitted := 0
	for _, t := range teams {
		tid := t.ID
		ok, cerr := h.authz.Check(ctx, auth.AuthzRequest{UserID: user.ID, Permission: perm, TeamScope: &tid})
		if cerr != nil {
			return errs.Internal(c, nil, cerr)
		}
		if !ok {
			continue // 该团队无 schedule.view，跳过（不汇总，不报错）
		}
		permitted++
		tl, lerr := h.schedules.OncallForTeam(ctx, tid)
		if lerr != nil {
			continue
		}
		lines = append(lines, tl...)
	}
	if permitted == 0 {
		// 所有团队都无 schedule.view → 越权，落审计 + 403（与其它拒绝路径一致）。
		h.auditDenied(c, user.ID, evt, ActionOncall, "no permission")
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "forbidden: no schedule.view in your teams"})
	}
	h.sendOncallResultCard(ctx, bot, evt.ChannelID, "", lines)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "command": "oncall"})
}

// oncallForNamed 处理带参 oncall：按 team/service 名解析目标团队 → 判 schedule.view → 查值班。
func (h *Handler) oncallForNamed(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent, arg string) error {
	// 先按 team 名解析目标团队 id 以判权限；未命中再按 service 名解析其归属团队。
	teamID, byTeam, found := h.resolveOncallScopeTeam(ctx, arg)
	if !found {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: fmt.Sprintf("no team or service named %q", arg)})
	}
	// 以目标团队 scope 判 schedule.view（团队级绑定必须带 scope 才生效）。
	user, err := h.resolveAndCheckScope(c, ctx, evt, ActionOncall, teamID)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	_ = user
	var lines []string
	if byTeam {
		lines, err = h.schedules.OncallForTeamName(ctx, arg)
	} else {
		lines, err = h.schedules.OncallForServiceName(ctx, arg)
	}
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: fmt.Sprintf("no schedule found for %q", arg)})
	}
	h.sendOncallResultCard(ctx, bot, evt.ChannelID, arg, lines)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "command": "oncall"})
}

// resolveOncallScopeTeam 把 oncall 参数（team 名或 service 名）解析成用于判权限的目标团队 id。
// 返回 (teamID, byTeam, found)：byTeam 标记命中的是 team 名（用于后续查值班走 team 还是 service）。
// team 名优先；未命中再按 service 名（取 service 归属团队作 scope）。都未命中 found=false。
// teamID 可能为 nil（资源无团队归属 → org 级判定），此时仍视为 found。
func (h *Handler) resolveOncallScopeTeam(ctx context.Context, name string) (teamID *int, byTeam, found bool) {
	if t, err := h.db.Team.Query().Where(entteam.NameEQ(name)).Only(ctx); err == nil {
		id := t.ID
		return &id, true, true
	}
	if svc, err := h.db.Service.Query().Where(entservice.NameEQ(name)).WithTeam().Only(ctx); err == nil {
		if svc.Edges.Team != nil {
			id := svc.Edges.Team.ID
			return &id, false, true
		}
		return nil, false, true // service 无团队归属 → org 级判定
	}
	return nil, false, false
}

// sendRunbookResultCard 把 Runbook 执行结果渲染为纯文本卡片回给触发者（无操作按钮）。
func (h *Handler) sendRunbookResultCard(ctx context.Context, bot IMBot, channel, rbName string, incID int, res *RunbookRunResult) {
	if channel == "" {
		return
	}
	card := &Card{
		Header:   fmt.Sprintf("Runbook「%s」执行结果", rbName),
		Severity: "info",
		Rows: []CardRow{
			{Label: "关联事件", Value: fmt.Sprintf("#%d", incID)},
		},
	}
	for _, s := range res.StepSummaries {
		card.Rows = append(card.Rows, CardRow{Label: "步骤", Value: s})
	}
	switch {
	case res.PendingApproval:
		// ★ 写步骤被审批闸门阻断：明确提示须回 Web 审批，绝不在 IM 内放行。
		card.StatusBadge = "⚠️ 含写操作步骤，需在 Web 审批后执行（IM 内不放行写操作）"
	case res.Aborted:
		card.StatusBadge = "⛔ 已中止：" + res.Reason
	default:
		card.StatusBadge = "✅ 只读诊断已执行"
	}
	if id, err := bot.SendCard(ctx, channel, card); err == nil {
		_ = id
	}
}

// sendOncallResultCard 把当前值班人渲染为纯文本卡片回给触发者（无操作按钮）。
func (h *Handler) sendOncallResultCard(ctx context.Context, bot IMBot, channel, scope string, lines []string) {
	if channel == "" {
		return
	}
	header := "当前值班"
	if scope != "" {
		header = fmt.Sprintf("当前值班（%s）", scope)
	}
	card := &Card{Header: header, Severity: "info"}
	for _, l := range lines {
		card.Rows = append(card.Rows, CardRow{Label: "值班", Value: l})
	}
	if len(card.Rows) == 0 {
		card.StatusBadge = "无在班人"
	}
	if id, err := bot.SendCard(ctx, channel, card); err == nil {
		_ = id
	}
}

// handleMention @人协同：把被 @的人加入 responders（拉人即授权）。
// 操作者需有 add_responder 权限；被拉的人无需在群里预先绑定。
func (h *Handler) handleMention(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	// 先从消息正文解析 incident id（约定 @机器人 <INC-id> 形式）。
	// ★ 必须在鉴权前解析：resolveAndCheck 按 evt.IncidentID 取 team scope 判权限；
	//   mention 事件的 IncidentID 不在 payload 顶层而在正文里，若不先回填，team scope 为 nil
	//   → 团队级 add_responder 绑定判不出（org 级判定），持团队权限者会被误拒（403）。
	incID, err := h.resolveIncidentArg(ctx, evt.Text)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	evt.IncidentID = strconv.Itoa(incID) // 回填供 resolveAndCheck 解 team scope
	user, err := h.resolveAndCheck(c, ctx, evt, ActionAddResponder)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	// 把被 @的 IM 用户映射成 User 并加入 responders
	for _, unionID := range evt.MentionAt {
		target, terr := h.mapper.ResolveUser(ctx, bot.Platform(), unionID)
		if terr != nil {
			continue // 未绑定的人跳过（可回提示让其先绑定）
		}
		if _, aerr := h.incSvc.AddResponder(ctx, incID, user.ID, target.ID, imincident.SourceIM); aerr != nil {
			return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: aerr.Error()})
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "responders added"})
}

// resolveAndCheck 账号映射 + 权限校验（IM 鉴权铁律 §6）。
// action 为按钮/命令对应的动作（ack/escalate/resolve/detail/add_responder），用于映射权限点。
// team scope 从 evt.IncidentID 解析（incident.team）。
// 每条拒绝路径（未绑定/无权限映射/越权）都落一条 denied 审计（S9）——IM 越权探测须与 Web 同样可追溯。
func (h *Handler) resolveAndCheck(c *echo.Context, ctx context.Context, evt *IMEvent, action string) (*ent.User, error) {
	teamScope, _ := h.incidentTeamScope(ctx, evt.IncidentID)
	return h.resolveAndCheckScope(c, ctx, evt, action, teamScope)
}

// resolveAndCheckScope 与 resolveAndCheck 同链路，但 team scope 由调用方显式给定。
// 供无 incident 关联但需团队 scope 判权限的命令用（如 /vigil oncall 按目标团队判 schedule.view）——
// 团队级权限绑定在 teamScope=nil 时不生效（authz 只查 org 级），故必须显式传入目标团队，
// 否则持团队级 schedule.view 者会被误判 org 级而拒（与 handleMention 回填 IncidentID 同理）。
func (h *Handler) resolveAndCheckScope(c *echo.Context, ctx context.Context, evt *IMEvent, action string, teamScope *int) (*ent.User, error) {
	// 1. im_unionid → User（未绑定拒绝）
	user, err := h.mapper.ResolveUser(ctx, evt.Platform, evt.UnionID)
	if err != nil {
		// actor 未知（未绑定），用 platform/union_id 溯源；这是典型越权探测面。
		h.auditDenied(c, 0, evt, action, "im account not bound")
		return nil, fmt.Errorf("im account not bound: %w", err)
	}
	// 2. action → 权限点
	perm, ok := PermissionMap[action]
	if !ok {
		h.auditDenied(c, user.ID, evt, action, "no permission mapping")
		return nil, fmt.Errorf("no permission mapping for action %q", action)
	}
	// 3. authz.Check（与 Web 同一链路）
	ok, err = h.authz.Check(ctx, auth.AuthzRequest{
		UserID:     user.ID,
		Permission: auth.Permission(perm),
		TeamScope:  teamScope,
	})
	if err != nil {
		return nil, fmt.Errorf("authz check: %w", err)
	}
	if !ok {
		// 已解析出 actor（user.ID），记谁越权尝试了什么动作（S9）。
		h.auditDenied(c, user.ID, evt, action, "no permission")
		return nil, errors.New("forbidden: no permission")
	}
	return user, nil
}

// auditDenied 记录一条 IM 越权拒绝审计（S9）。
// actorID=0 表示账号未绑定（actor 未知，用 platform/union_id 溯源）。
func (h *Handler) auditDenied(c *echo.Context, actorID int, evt *IMEvent, action, reason string) {
	if h.audit == nil {
		return
	}
	incID, _ := strconv.Atoi(evt.IncidentID)
	e := auth.AuditEntryFromRequest(c.Request(), actorID, "")
	e.Action = auth.ActionIMDenied
	e.ResourceType = "incident"
	e.ResourceID = incID
	e.Result = auth.AuditResultDenied
	e.Detail = map[string]any{
		"platform":  evt.Platform,
		"union_id":  evt.UnionID,
		"im_action": action,
		"reason":    reason,
	}
	h.audit.MustRecord(c.Request().Context(), e)
}

// incidentTeamScope 取 incident 归属团队作为鉴权 scope。
func (h *Handler) incidentTeamScope(ctx context.Context, incidentIDStr string) (*int, error) {
	incID, err := strconv.Atoi(incidentIDStr)
	if err != nil || incID == 0 {
		return nil, nil
	}
	inc, err := h.db.Incident.Get(ctx, incID)
	if err != nil {
		return nil, nil // incident 不存在则 org 级判定
	}
	t, err := inc.QueryTeam().Only(ctx)
	if err != nil || t == nil {
		return nil, nil // 无团队则 org 级
	}
	return &t.ID, nil
}

// resolveIncidentArg 从命令参数解析 incident：支持数字 ID 或 INC-xxxx 编号。
func (h *Handler) resolveIncidentArg(ctx context.Context, arg string) (int, error) {
	arg = firstToken(strings.TrimSpace(arg))
	if arg == "" {
		return 0, errors.New("incident id required")
	}
	// 纯数字：直接当 ID
	if id, err := strconv.Atoi(arg); err == nil {
		return id, nil
	}
	// INC-xxxx 形式：按 number 精确查
	inc, err := h.db.Incident.Query().Where(incident.NumberEQ(arg)).Only(ctx)
	if err != nil {
		return 0, fmt.Errorf("cannot resolve incident %q: %w", arg, err)
	}
	return inc.ID, nil
}

// refreshCard 状态变更后刷新已发卡片（若有记录的 cardID）。
func (h *Handler) refreshCard(ctx context.Context, bot IMBot, inc *ent.Incident, user *ent.User) {
	if inc == nil {
		return
	}
	cardID, ok := h.cards.Get(ctx, inc.ID, bot.Platform())
	if !ok {
		return // 无已发卡片记录（可能是命令触发，非卡片按钮）
	}
	card := BuildCard(inc, user.Name)
	// B16：状态徽章标注最新状态 + 操作者。钉钉降级为发新消息时，这条徽章就是群内可见的
	// 「⚠️ INC-xxx 已 acked by 张三」状态变更提示（飞书则原地刷新到卡片顶部）。
	card.StatusBadge = fmt.Sprintf("⚠️ %s %s（by %s）", inc.Number, statusLabelCN(inc.Status), user.Name)
	// 刷新后的卡片不再显示已完成的动作按钮（保持简洁）
	if err := bot.UpdateCard(ctx, cardID, card); err != nil {
		// 卡片更新失败不阻塞主流程（状态已落库）。钉钉无 channel 无法降级重发时也走这里。
		_ = err
	}
}

// sendCardToUser 给某 channel 发一张当前状态卡片，并记录 cardID。
func (h *Handler) sendCardToUser(ctx context.Context, bot IMBot, inc *ent.Incident, channel string, user *ent.User) {
	card := BuildCard(inc, user.Name)
	if h.renderer != nil {
		teamScope, _ := h.incidentTeamScope(ctx, fmt.Sprintf("%d", inc.ID))
		_ = h.renderer.WithPermittedButtons(card, user.ID, teamScope, DefaultButtons())
	}
	cardID, err := bot.SendCard(ctx, channel, card)
	if err == nil {
		h.cards.Put(ctx, inc.ID, bot.Platform(), cardID)
	}
}

// commandToAction 斜杠命令 → 动作（用于权限点映射）。
// 注：runbook / oncall 命令在 handleCommand 前置分支处理，不经此表（参数形态不同），
//
//	此处仍登记以保持命令→动作映射完整（便于集中查阅）。
func commandToAction(cmd string) string {
	switch cmd {
	case "ack":
		return ActionAck
	case "escalate":
		return ActionEscalate
	case "resolve":
		return ActionResolve
	case "add":
		return ActionAddResponder
	case "status":
		return ActionDetail
	case "runbook":
		return ActionRunbook
	case "oncall":
		return ActionOncall
	default:
		return ""
	}
}

// splitTwo 把命令参数按首个空白切成两段（如 "<runbook> <incident>"）。
// 第二段保留剩余全部文本（去首尾空白）；无第二段则第二段为空。
func splitTwo(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	parts := strings.SplitN(s, " ", 2)
	first = parts[0]
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	return first, rest
}

var _ = ActionAddResponder // 保留引用（commandToAction 与 handleMention 使用）

// replyErr 把鉴权/解析错误通过 IM 消息反馈给用户（而非仅 HTTP 错误码）。
func replyErr(c *echo.Context, err error, bot IMBot, channel string) error {
	// 鉴权失败统一返回 403（IM 端可由机器人另行友好提示）
	if errors.Is(err, ErrNotBound) {
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "im account not bound, please bind in web"})
	}
	return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: err.Error()})
}

// readBody 读取并复制请求 body（保留原 body 供后续 echo 绑定）。
func readBody(c *echo.Context) ([]byte, error) {
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	// 还原 body，避免后续中间件读不到
	c.Request().Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}

// headerMap 把 http.Header 转成 map（适配器按需取 X-Lark-Signature 等）。
func headerMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k := range h {
		m[k] = h.Get(k)
	}
	return m
}

// isChallenge 判断是否飞书 URL 验证 challenge 请求。
func isChallenge(payload []byte) bool {
	return bytes.Contains(payload, []byte(`"challenge"`)) && bytes.Contains(payload, []byte(`"url_verification"`))
}

// echoChallenge 回显 challenge（飞书要求原样返回 challenge 字段）。
func echoChallenge(payload []byte) map[string]string {
	var req struct {
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(payload, &req)
	return map[string]string{"challenge": req.Challenge}
}

// firstToken 取字符串第一个空白分隔的 token。
func firstToken(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return s[:i]
		}
	}
	return s
}

// statusLabelCN incident status 中文（与 card.go statusLabel 同义，handler 独立维护避免循环）。
func statusLabelCN(status incident.Status) string {
	switch string(status) {
	case "triggered":
		return "待响应"
	case "escalated":
		return "已升级"
	case "acked":
		return "已确认"
	case "resolved":
		return "已解决"
	case "closed":
		return "已关闭"
	}
	return string(status)
}
