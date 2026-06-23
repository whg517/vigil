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
	"sync"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/httputil"
	imincident "github.com/kevin/vigil/internal/incident"

	"github.com/labstack/echo/v5"
)

// CardStore 记录 incident → 已发卡片 ID 的映射，供状态变更后 UpdateCard。
// 本期为进程内 map（重启丢失，可重发卡片兜底）；生产可换 Redis 持久化。
type CardStore struct {
	mu    sync.RWMutex
	cards map[int]map[string]string // incidentID → platform → cardID
}

// NewCardStore 创建卡片 ID 存储。
func NewCardStore() *CardStore {
	return &CardStore{cards: make(map[int]map[string]string)}
}

// Put 记录某 incident 在某平台下发的卡片 ID。
func (s *CardStore) Put(incidentID int, platform, cardID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cards[incidentID] == nil {
		s.cards[incidentID] = make(map[string]string)
	}
	s.cards[incidentID][platform] = cardID
}

// Get 取某 incident 在某平台的卡片 ID。
func (s *CardStore) Get(incidentID int, platform string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.cards[incidentID]; ok {
		if id, ok2 := m[platform]; ok2 {
			return id, true
		}
	}
	return "", false
}

// Handler IM Webhook 回调与卡片操作链路。
type Handler struct {
	db       *ent.Client
	registry *Registry
	mapper   *Mapper
	authz    *auth.Authorizer
	incSvc   *imincident.Service
	renderer *Renderer
	cards    *CardStore
}

// NewHandler 创建 IM handler。
// hasPermission 为权限判定回调（通常包装 auth.Authorizer.CheckAny）。
func NewHandler(
	db *ent.Client,
	registry *Registry,
	mapper *Mapper,
	authz *auth.Authorizer,
	incSvc *imincident.Service,
	renderer *Renderer,
	cards *CardStore,
) *Handler {
	return &Handler{
		db: db, registry: registry, mapper: mapper, authz: authz,
		incSvc: incSvc, renderer: renderer, cards: cards,
	}
}

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
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}

	// 刷新已发卡片（若有记录的 cardID）
	h.refreshCard(ctx, bot, inc, user)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "action": evt.Action})
}

// handleCommand 斜杠命令：/vigil ack INC-0042 等。
// 命令格式：<command> <incident_id|number> [args]
func (h *Handler) handleCommand(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
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
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	h.refreshCard(ctx, bot, inc, user)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "command": cmd})
}

// handleMention @人协同：把被 @的人加入 responders（拉人即授权）。
// 操作者需有 add_responder 权限；被拉的人无需在群里预先绑定。
func (h *Handler) handleMention(c *echo.Context, ctx context.Context, bot IMBot, evt *IMEvent) error {
	user, err := h.resolveAndCheck(c, ctx, evt, ActionAddResponder)
	if err != nil {
		return replyErr(c, err, bot, evt.ChannelID)
	}
	// 从消息正文解析 incident id（约定 @机器人 <INC-id> 形式）
	incID, err := h.resolveIncidentArg(ctx, evt.Text)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
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
func (h *Handler) resolveAndCheck(c *echo.Context, ctx context.Context, evt *IMEvent, action string) (*ent.User, error) {
	// 1. im_unionid → User（未绑定拒绝）
	user, err := h.mapper.ResolveUser(ctx, evt.Platform, evt.UnionID)
	if err != nil {
		return nil, fmt.Errorf("im account not bound: %w", err)
	}
	// 2. action → 权限点
	perm, ok := PermissionMap[action]
	if !ok {
		return nil, fmt.Errorf("no permission mapping for action %q", action)
	}
	// 3. 解析资源 scope（incident.team）
	teamScope, _ := h.incidentTeamScope(ctx, evt.IncidentID)
	// 4. authz.Check（与 Web 同一链路）
	ok, err = h.authz.Check(ctx, auth.AuthzRequest{
		UserID:     user.ID,
		Permission: auth.Permission(perm),
		TeamScope:  teamScope,
	})
	if err != nil {
		return nil, fmt.Errorf("authz check: %w", err)
	}
	if !ok {
		return nil, errors.New("forbidden: no permission")
	}
	return user, nil
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
	cardID, ok := h.cards.Get(inc.ID, bot.Platform())
	if !ok {
		return // 无已发卡片记录（可能是命令触发，非卡片按钮）
	}
	card := BuildCard(inc, user.Name)
	card.StatusBadge = fmt.Sprintf("%s 操作（by %s）", statusLabelCN(inc.Status), user.Name)
	// 刷新后的卡片不再显示已完成的动作按钮（保持简洁）
	if err := bot.UpdateCard(ctx, cardID, card); err != nil {
		// 卡片更新失败不阻塞主流程（状态已落库）
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
		h.cards.Put(inc.ID, bot.Platform(), cardID)
	}
}

// commandToAction 斜杠命令 → 动作（用于权限点映射）。
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
	default:
		return ""
	}
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
