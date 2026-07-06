// handler.go 通知配置 API（能力域 7 + 能力域 3 抑制）。
//
// 暴露 NotificationRule 与 SuppressionRule 的 CRUD：
//
//	GET/POST/PATCH/DELETE /notification-rules
//	GET/POST/PATCH/DELETE /suppression-rules
//	POST /notification-rules/:id/test   dry-run：对某 incident 评估是否静默/聚合
//
// 权限：这些路由本身需由调用方在装配时按 notification.rule.* / suppression.* 鉴权
// （与 auth.Handler 一致：Register 不内嵌权限中间件，由 main 用子 group 挂）。
package notification

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/notificationrule"
	"github.com/kevin/vigil/ent/notificationtemplate"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/suppressionrule"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行写操作（规则/抑制/模板增改删），造成"报 403 却已落库"的越权。故
// checkAccess 拒绝时返回本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler 通知配置 API（规则 + 抑制 + 模板）。
type Handler struct {
	db         *ent.Client
	notifier   *Notifier // 用于 test 端点 dry-run
	aggregator *Aggregator
	templates  *TemplateEngine     // 用于 preview 端点
	authz      *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope      *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建通知配置 handler。notifier/aggregator/templates 可为 nil（对应端点降级）。
func NewHandler(db *ent.Client, notifier *Notifier, aggregator *Aggregator) *Handler {
	return &Handler{db: db, notifier: notifier, aggregator: aggregator}
}

// SetTemplateEngine 注入模板引擎（template CRUD + preview 需要）。
func (h *Handler) SetTemplateEngine(e *TemplateEngine) {
	h.templates = e
}

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 kind 资源是否有 perm 权限。
// kind 取值：notification_rule / suppression_rule / notification_template。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission, kind string) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, kind, id)
	if err != nil {
		// errs.Internal 写完 500 返回 nil，必须换成非 nil 哨兵，否则调用方不会中止。
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		// 同理：errs.Forbidden 写完 403 返回 nil，返回哨兵让调用方 return 中止后续写操作。
		_ = errs.Forbidden(c, "")
		return errAccessDenied
	}
	return nil
}

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	// NotificationRule CRUD
	g.GET("/notification-rules", h.listRules)
	g.POST("/notification-rules", h.createRule)
	g.GET("/notification-rules/:id", h.getRule)
	g.PATCH("/notification-rules/:id", h.updateRule)
	g.DELETE("/notification-rules/:id", h.deleteRule)
	g.POST("/notification-rules/:id/test", h.testRule) // dry-run

	// SuppressionRule CRUD
	g.GET("/suppression-rules", h.listSuppressions)
	g.POST("/suppression-rules", h.createSuppression)
	g.GET("/suppression-rules/:id", h.getSuppression)
	g.PATCH("/suppression-rules/:id", h.updateSuppression)
	g.DELETE("/suppression-rules/:id", h.deleteSuppression)

	// NotificationTemplate CRUD + preview
	g.GET("/notification-templates", h.listTemplates)
	g.POST("/notification-templates", h.createTemplate)
	g.GET("/notification-templates/:id", h.getTemplate)
	g.PATCH("/notification-templates/:id", h.updateTemplate)
	g.DELETE("/notification-templates/:id", h.deleteTemplate)
	g.POST("/notification-templates/:id/preview", h.previewTemplate) // 传 incident_id 渲染预览
}

// ===== NotificationRule =====

// ListNotificationRules 列出全部通知规则。
//
// @Summary      List notification rules
// @Description  返回全部 NotificationRule（无分页）。
// @Tags         notification
// @Produce      json
// @Success      200  {array}  ent.NotificationRule
// @Failure      500  {object} httputil.ErrorResponse
// @Router       /notification-rules [get]
// @Security     bearerAuth
func (h *Handler) listRules(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.NotificationRule.Query()
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// org 级用户（orgWide）全可见；team 级用户仅可见 binding 的 team；无 binding 返回空。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.NotificationRule{})
				}
				q = q.Where(notificationrule.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	rules, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, rules)
}

type createRuleReq struct {
	Name       string         `json:"name"`
	Condition  map[string]any `json:"condition"`
	Channels   []string       `json:"channels"`
	TemplateID string         `json:"template_id"`
	QuietHours map[string]any `json:"quiet_hours"`
	TeamID     int            `json:"team_id"`
	Enabled    *bool          `json:"enabled"`
}

// CreateNotificationRule 创建通知规则。
//
// @Summary      Create notification rule
// @Description  新建 NotificationRule（条件/渠道/静默时段等）。
// @Tags         notification
// @Accept       json
// @Produce      json
// @Param        request  body      createRuleReq  true  "通知规则定义"
// @Success      201      {object}  ent.NotificationRule
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /notification-rules [post]
// @Security     bearerAuth
func (h *Handler) createRule(c *echo.Context) error {
	var req createRuleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	if len(req.Channels) == 0 {
		req.Channels = []string{"im", "webhook"}
	}
	b := h.db.NotificationRule.Create().
		SetName(req.Name).
		SetCondition(req.Condition).
		SetChannels(req.Channels).
		SetQuietHours(req.QuietHours)
	if req.TemplateID != "" {
		b.SetTemplateID(req.TemplateID)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if req.Enabled != nil {
		b.SetEnabled(*req.Enabled)
	}
	r, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "notification rule", "notification rule already exists")
	}
	return c.JSON(http.StatusCreated, r)
}

// GetNotificationRule 获取单个通知规则。
//
// @Summary      Get notification rule
// @Description  按 ID 取得 NotificationRule。
// @Tags         notification
// @Produce      json
// @Param        id   path      int  true  "规则 ID"
// @Success      200  {object}  ent.NotificationRule
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /notification-rules/{id} [get]
// @Security     bearerAuth
func (h *Handler) getRule(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationRuleView, "notification_rule"); e != nil {
		return e
	}
	r, err := h.db.NotificationRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, r)
}

type updateRuleReq struct {
	Name       *string         `json:"name"`
	Condition  *map[string]any `json:"condition"`
	Channels   *[]string       `json:"channels"`
	TemplateID *string         `json:"template_id"`
	QuietHours *map[string]any `json:"quiet_hours"`
	Enabled    *bool           `json:"enabled"`
}

// UpdateNotificationRule 更新通知规则（部分字段）。
//
// @Summary      Update notification rule
// @Description  按 ID 部分更新 NotificationRule。
// @Tags         notification
// @Accept       json
// @Produce      json
// @Param        id       path      int             true  "规则 ID"
// @Param        request  body      updateRuleReq   true  "待更新字段"
// @Success      200      {object}  ent.NotificationRule
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /notification-rules/{id} [patch]
// @Security     bearerAuth
func (h *Handler) updateRule(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationRuleView, "notification_rule"); e != nil {
		return e
	}
	var req updateRuleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	upd := h.db.NotificationRule.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Condition != nil {
		upd.SetCondition(*req.Condition)
	}
	if req.Channels != nil {
		upd.SetChannels(*req.Channels)
	}
	if req.TemplateID != nil {
		upd.SetTemplateID(*req.TemplateID)
	}
	if req.QuietHours != nil {
		upd.SetQuietHours(*req.QuietHours)
	}
	if req.Enabled != nil {
		upd.SetEnabled(*req.Enabled)
	}
	r, err := upd.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, r)
}

// DeleteNotificationRule 删除通知规则。
//
// @Summary      Delete notification rule
// @Description  按 ID 删除 NotificationRule。
// @Tags         notification
// @Param        id   path  int  true  "规则 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /notification-rules/{id} [delete]
// @Security     bearerAuth
func (h *Handler) deleteRule(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationRuleView, "notification_rule"); e != nil {
		return e
	}
	if err := h.db.NotificationRule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// TestNotificationRule dry-run：给定 incident_id，评估该规则是否对其静默。
// 返回 {quiet_hours_suppress: bool, quiet_hours_config: {...}, channels: [...]}。
//
// @Summary      Test notification rule (dry-run)
// @Description  对给定 incident_id 评估该规则是否对其静默（dry-run，无副作用）。
// @Tags         notification
// @Produce      json
// @Param        id           path   int     true   "规则 ID"
// @Param        incident_id  query  int     false  "待评估 incident ID"
// @Success      200          {object}  map[string]any
// @Failure      400          {object}  httputil.ErrorResponse
// @Failure      404          {object}  httputil.ErrorResponse
// @Failure      500          {object}  httputil.ErrorResponse
// @Router       /notification-rules/{id}/test [post]
// @Security     bearerAuth
func (h *Handler) testRule(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationRuleView, "notification_rule"); e != nil {
		return e
	}
	incIDStr := c.QueryParam("incident_id")
	incID, _ := strconv.Atoi(incIDStr)
	r, err := h.db.NotificationRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "rule not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	// 解析静默配置
	qh := parseQuietHours(r.QuietHours)
	resp := map[string]any{
		"rule_id":   r.ID,
		"rule_name": r.Name,
		"channels":  r.Channels,
	}
	if incID > 0 {
		inc, err := h.db.Incident.Get(c.Request().Context(), incID)
		if err != nil {
			resp["error"] = "incident not found"
			return c.JSON(http.StatusOK, resp)
		}
		if qh != nil {
			// test 不区分 oncall（无 target 上下文），按非值班人判定
			resp["quiet_hours_suppress"] = qh.ShouldSuppress(string(inc.Severity), false, nil)
			resp["quiet_hours"] = qh
		}
		resp["severity"] = inc.Severity
	}
	return c.JSON(http.StatusOK, resp)
}

// parseQuietHours 把 NotificationRule.quiet_hours (map[string]any) 解析为 QuietHours。
// 字段名与 capabilities/04 §5 对齐。
func parseQuietHours(m map[string]any) *QuietHours {
	return ParseQuietHoursPublic(m)
}

// ParseQuietHoursPublic 导出版本，供 main 装配静默解析器复用。
func ParseQuietHoursPublic(m map[string]any) *QuietHours {
	if len(m) == 0 {
		return nil
	}
	qh := &QuietHours{}
	if v, ok := m["enabled"].(bool); ok {
		qh.Enabled = v
	}
	if v, ok := m["start"].(string); ok {
		qh.Start = v
	}
	if v, ok := m["end"].(string); ok {
		qh.End = v
	}
	if v, ok := m["timezone"].(string); ok {
		qh.Timezone = v
	}
	if arr, ok := m["bypass_for"].([]any); ok {
		for _, a := range arr {
			if s, ok := a.(string); ok {
				qh.BypassFor = append(qh.BypassFor, s)
			}
		}
	}
	return qh
}

// ===== SuppressionRule =====

// ListSuppressionRules 列出全部抑制规则。
//
// @Summary      List suppression rules
// @Description  返回全部 SuppressionRule（无分页）。可用 ?kind=maintenance|adhoc 过滤，便于前端维护窗口专属列表。
// @Tags         suppression
// @Produce      json
// @Param        kind  query     string  false  "按类别过滤：adhoc（日常降噪）| maintenance（维护窗口）"
// @Success      200  {array}  ent.SuppressionRule
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /suppression-rules [get]
// @Security     bearerAuth
func (h *Handler) listSuppressions(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.SuppressionRule.Query()
	// kind 过滤：前端维护窗口专属列表用 ?kind=maintenance。非法值返 400。
	switch kind := c.QueryParam("kind"); kind {
	case "":
		// 不过滤
	case "adhoc":
		q = q.Where(suppressionrule.KindEQ(suppressionrule.KindAdhoc))
	case "maintenance":
		q = q.Where(suppressionrule.KindEQ(suppressionrule.KindMaintenance))
	default:
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid kind (want adhoc|maintenance)"})
	}
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// org 级用户（orgWide）全可见；team 级用户仅可见 binding 的 team；无 binding 返回空。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.SuppressionRule{})
				}
				q = q.Where(suppressionrule.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	rules, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, rules)
}

type createSuppressionReq struct {
	Name             string            `json:"name"`
	Kind             string            `json:"kind"` // adhoc（默认）| maintenance
	MatchLabels      map[string]string `json:"match_labels"`
	TimeWindow       map[string]any    `json:"time_window"`
	SeverityFilter   []string          `json:"severity_filter"`
	Action           string            `json:"action"`
	ReduceTo         string            `json:"reduce_to"`
	PreserveCritical *bool             `json:"preserve_critical"`
	TeamID           int               `json:"team_id"`
	Enabled          *bool             `json:"enabled"`
	ExpiresAt        *string           `json:"expires_at"`
}

// validateTimeWindow 校验 time_window 的 {start,end}（维护窗口计划起止）：
//   - 两者要么都提供、要么都省略；只给其一视为非法。
//   - 均须 RFC3339 格式，且 start < end。
//   - {expires_at} 等其它 key 不在此校验（expires_at 走独立字段）。
//
// 返回非空 string 为错误原因（供上层返 400）；空 string 表示合法。
func validateTimeWindow(tw map[string]any) string {
	if tw == nil {
		return ""
	}
	startRaw, hasStart := tw["start"]
	endRaw, hasEnd := tw["end"]
	// 全空 → 合法（无窗口限制）。
	startStr, _ := startRaw.(string)
	endStr, _ := endRaw.(string)
	if (!hasStart || startStr == "") && (!hasEnd || endStr == "") {
		return ""
	}
	if startStr == "" || endStr == "" {
		return "time_window requires both start and end (RFC3339)"
	}
	start, err1 := time.Parse(time.RFC3339, startStr)
	if err1 != nil {
		return "invalid time_window.start (want RFC3339)"
	}
	end, err2 := time.Parse(time.RFC3339, endStr)
	if err2 != nil {
		return "invalid time_window.end (want RFC3339)"
	}
	if !start.Before(end) {
		return "time_window.start must be before end"
	}
	return ""
}

// CreateSuppressionRule 创建抑制规则。
//
// @Summary      Create suppression rule
// @Description  新建 SuppressionRule（标签匹配/时间窗/严重度过滤等）。
// @Tags         suppression
// @Accept       json
// @Produce      json
// @Param        request  body      createSuppressionReq  true  "抑制规则定义"
// @Success      201      {object}  ent.SuppressionRule
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /suppression-rules [post]
// @Security     bearerAuth
func (h *Handler) createSuppression(c *echo.Context) error {
	var req createSuppressionReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	action := suppressionrule.ActionSuppress
	if req.Action == "reduce_severity" {
		action = suppressionrule.ActionReduceSeverity
	}
	// kind：默认 adhoc；maintenance=计划内维护窗口。非法值返 400。
	var kind suppressionrule.Kind
	switch req.Kind {
	case "", "adhoc":
		kind = suppressionrule.KindAdhoc
	case "maintenance":
		kind = suppressionrule.KindMaintenance
	default:
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid kind (want adhoc|maintenance)"})
	}
	// time_window 计划起止校验（维护窗口靠 {start,end} 表达；start<end、RFC3339）。
	if msg := validateTimeWindow(req.TimeWindow); msg != "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: msg})
	}
	b := h.db.SuppressionRule.Create().
		SetName(req.Name).
		SetKind(kind).
		SetMatchLabels(req.MatchLabels).
		SetAction(action)
	if req.TimeWindow != nil {
		b.SetTimeWindow(req.TimeWindow)
	}
	if len(req.SeverityFilter) > 0 {
		b.SetSeverityFilter(req.SeverityFilter)
	}
	if req.ReduceTo != "" {
		b.SetReduceTo(req.ReduceTo)
	}
	if req.PreserveCritical != nil {
		b.SetPreserveCritical(*req.PreserveCritical)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if req.Enabled != nil {
		b.SetEnabled(*req.Enabled)
	}
	// B15：expires_at 可通过 API 设置——过期规则在评估时不命中（见 SuppressionEngine.Evaluate）。
	// 空字符串视为不设置（永久生效，靠 enabled 控制启停）；非法时间返 400。
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		exp, perr := time.Parse(time.RFC3339, *req.ExpiresAt)
		if perr != nil {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid expires_at (want RFC3339)"})
		}
		b.SetExpiresAt(exp)
	}
	r, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "suppression rule", "suppression rule already exists")
	}
	return c.JSON(http.StatusCreated, r)
}

// GetSuppressionRule 获取单个抑制规则。
//
// @Summary      Get suppression rule
// @Description  按 ID 取得 SuppressionRule。
// @Tags         suppression
// @Produce      json
// @Param        id   path      int  true  "规则 ID"
// @Success      200  {object}  ent.SuppressionRule
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /suppression-rules/{id} [get]
// @Security     bearerAuth
func (h *Handler) getSuppression(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermSuppressionView, "suppression_rule"); e != nil {
		return e
	}
	r, err := h.db.SuppressionRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, r)
}

type updateSuppressionReq struct {
	Name             *string            `json:"name"`
	Kind             *string            `json:"kind"` // adhoc | maintenance
	MatchLabels      *map[string]string `json:"match_labels"`
	TimeWindow       *map[string]any    `json:"time_window"`
	SeverityFilter   *[]string          `json:"severity_filter"`
	Action           *string            `json:"action"`
	ReduceTo         *string            `json:"reduce_to"`
	PreserveCritical *bool              `json:"preserve_critical"`
	Enabled          *bool              `json:"enabled"`
	// ExpiresAt B15：RFC3339 时间设置过期；显式传空串清除过期（改回永久生效）。
	ExpiresAt *string `json:"expires_at"`
}

// UpdateSuppressionRule 更新抑制规则（部分字段）。
//
// @Summary      Update suppression rule
// @Description  按 ID 部分更新 SuppressionRule。
// @Tags         suppression
// @Accept       json
// @Produce      json
// @Param        id       path      int                    true  "规则 ID"
// @Param        request  body      updateSuppressionReq   true  "待更新字段"
// @Success      200      {object}  ent.SuppressionRule
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /suppression-rules/{id} [patch]
// @Security     bearerAuth
func (h *Handler) updateSuppression(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermSuppressionView, "suppression_rule"); e != nil {
		return e
	}
	var req updateSuppressionReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	// kind：可选切换 adhoc/maintenance；非法值返 400。
	if req.Kind != nil {
		switch *req.Kind {
		case "adhoc":
			// 由 upd 下方统一设置
		case "maintenance":
		default:
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid kind (want adhoc|maintenance)"})
		}
	}
	// time_window 计划起止校验（若本次带了 time_window）。
	if req.TimeWindow != nil {
		if msg := validateTimeWindow(*req.TimeWindow); msg != "" {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: msg})
		}
	}
	upd := h.db.SuppressionRule.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Kind != nil {
		switch *req.Kind {
		case "adhoc":
			upd.SetKind(suppressionrule.KindAdhoc)
		case "maintenance":
			upd.SetKind(suppressionrule.KindMaintenance)
		}
	}
	if req.MatchLabels != nil {
		upd.SetMatchLabels(*req.MatchLabels)
	}
	if req.TimeWindow != nil {
		upd.SetTimeWindow(*req.TimeWindow)
	}
	if req.SeverityFilter != nil {
		upd.SetSeverityFilter(*req.SeverityFilter)
	}
	if req.Action != nil {
		switch *req.Action {
		case "suppress":
			upd.SetAction(suppressionrule.ActionSuppress)
		case "reduce_severity":
			upd.SetAction(suppressionrule.ActionReduceSeverity)
		}
	}
	if req.ReduceTo != nil {
		upd.SetReduceTo(*req.ReduceTo)
	}
	if req.PreserveCritical != nil {
		upd.SetPreserveCritical(*req.PreserveCritical)
	}
	if req.Enabled != nil {
		upd.SetEnabled(*req.Enabled)
	}
	// B15：expires_at 可改——空串清除（改回永久），有值则按 RFC3339 设置。
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == "" {
			upd.ClearExpiresAt()
		} else {
			exp, perr := time.Parse(time.RFC3339, *req.ExpiresAt)
			if perr != nil {
				return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid expires_at (want RFC3339)"})
			}
			upd.SetExpiresAt(exp)
		}
	}
	r, err := upd.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, r)
}

// DeleteSuppressionRule 删除抑制规则。
//
// @Summary      Delete suppression rule
// @Description  按 ID 删除 SuppressionRule。
// @Tags         suppression
// @Param        id   path  int  true  "规则 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /suppression-rules/{id} [delete]
// @Security     bearerAuth
func (h *Handler) deleteSuppression(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermSuppressionView, "suppression_rule"); e != nil {
		return e
	}
	if err := h.db.SuppressionRule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ===== NotificationTemplate =====

// ListNotificationTemplates 列出全部通知模板。
//
// @Summary      List notification templates
// @Description  返回全部 NotificationTemplate（无分页）。
// @Tags         notification
// @Produce      json
// @Success      200  {array}  ent.NotificationTemplate
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /notification-templates [get]
// @Security     bearerAuth
func (h *Handler) listTemplates(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.NotificationTemplate.Query()
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// 注意：notification_template 的 team 可能为 nil（builtin 全局模板），ScopeResolver
	// 对 nil team 返回 nil → org 级判定；list 过滤此处仅作用于绑定 team 的模板。
	// org 级用户（orgWide）全可见；team 级用户仅可见 binding 的 team；无 binding 返回空。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.NotificationTemplate{})
				}
				q = q.Where(notificationtemplate.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	templates, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, templates)
}

type createTemplateReq struct {
	Name          string           `json:"name"`
	Channel       string           `json:"channel"` // im | email | webhook | phone | sms
	Format        string           `json:"format"`  // text | interactive_card
	TitleTemplate string           `json:"title_template"`
	BodyTemplate  string           `json:"body_template"`
	Actions       []map[string]any `json:"actions"`
	TeamID        int              `json:"team_id"`
	Builtin       *bool            `json:"builtin"`
}

// CreateNotificationTemplate 创建通知模板。
//
// @Summary      Create notification template
// @Description  新建 NotificationTemplate（渠道/格式/标题/正文模板/动作）。
// @Tags         notification
// @Accept       json
// @Produce      json
// @Param        request  body      createTemplateReq  true  "模板定义"
// @Success      201      {object}  ent.NotificationTemplate
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /notification-templates [post]
// @Security     bearerAuth
func (h *Handler) createTemplate(c *echo.Context) error {
	var req createTemplateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.TitleTemplate == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and title_template required"})
	}
	b := h.db.NotificationTemplate.Create().
		SetName(req.Name).
		SetChannel(notificationtemplate.Channel(req.Channel)).
		SetFormat(notificationtemplate.Format(req.Format)).
		SetTitleTemplate(req.TitleTemplate).
		SetBodyTemplate(req.BodyTemplate)
	if len(req.Actions) > 0 {
		b.SetActions(parseTemplateActions(req.Actions))
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if req.Builtin != nil {
		b.SetBuiltin(*req.Builtin)
	}
	t, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "notification template", "notification template already exists")
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.JSON(http.StatusCreated, t)
}

// GetNotificationTemplate 获取单个通知模板。
//
// @Summary      Get notification template
// @Description  按 ID 取得 NotificationTemplate。
// @Tags         notification
// @Produce      json
// @Param        id   path      int  true  "模板 ID"
// @Success      200  {object}  ent.NotificationTemplate
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /notification-templates/{id} [get]
// @Security     bearerAuth
func (h *Handler) getTemplate(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationTemplateView, "notification_template"); e != nil {
		return e
	}
	t, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, t)
}

type updateTemplateReq struct {
	Name          *string           `json:"name"`
	Channel       *string           `json:"channel"`
	Format        *string           `json:"format"`
	TitleTemplate *string           `json:"title_template"`
	BodyTemplate  *string           `json:"body_template"`
	Actions       *[]map[string]any `json:"actions"`
}

// UpdateNotificationTemplate 更新通知模板（内置模板禁止修改）。
//
// @Summary      Update notification template
// @Description  按 ID 部分更新 NotificationTemplate；内置模板返回 403。
// @Tags         notification
// @Accept       json
// @Produce      json
// @Param        id       path      int                 true  "模板 ID"
// @Param        request  body      updateTemplateReq   true  "待更新字段"
// @Success      200      {object}  ent.NotificationTemplate
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      403      {object}  httputil.ErrorResponse
// @Failure      404      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /notification-templates/{id} [patch]
// @Security     bearerAuth
func (h *Handler) updateTemplate(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationTemplateView, "notification_template"); e != nil {
		return e
	}
	// 内置模板不可改（只能由 seed 更新）
	existing, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if existing.Builtin {
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "builtin template cannot be modified"})
	}
	var req updateTemplateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	upd := h.db.NotificationTemplate.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Channel != nil {
		upd.SetChannel(notificationtemplate.Channel(*req.Channel))
	}
	if req.Format != nil {
		upd.SetFormat(notificationtemplate.Format(*req.Format))
	}
	if req.TitleTemplate != nil {
		upd.SetTitleTemplate(*req.TitleTemplate)
	}
	if req.BodyTemplate != nil {
		upd.SetBodyTemplate(*req.BodyTemplate)
	}
	if req.Actions != nil {
		upd.SetActions(parseTemplateActions(*req.Actions))
	}
	t, err := upd.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.JSON(http.StatusOK, t)
}

// DeleteNotificationTemplate 删除通知模板（内置模板禁止删除）。
//
// @Summary      Delete notification template
// @Description  按 ID 删除 NotificationTemplate；内置模板返回 403。
// @Tags         notification
// @Param        id   path  int  true  "模板 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      403  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /notification-templates/{id} [delete]
// @Security     bearerAuth
func (h *Handler) deleteTemplate(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationTemplateView, "notification_template"); e != nil {
		return e
	}
	existing, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if existing.Builtin {
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "builtin template cannot be deleted"})
	}
	if err := h.db.NotificationTemplate.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.NoContent(http.StatusNoContent)
}

// PreviewNotificationTemplate 传 incident_id，返回该模板渲染后的 title/body。
// 所见即所得，用于模板编辑器预览。
//
// @Summary      Preview notification template
// @Description  按 incident_id 渲染 NotificationTemplate，返回 title/body 预览。
// @Tags         notification
// @Produce      json
// @Param        id           path   int     true   "模板 ID"
// @Param        incident_id  query  int     true   "用于渲染上下文的 incident ID"
// @Success      200          {object}  map[string]any
// @Failure      400          {object}  httputil.ErrorResponse
// @Failure      404          {object}  httputil.ErrorResponse
// @Failure      500          {object}  httputil.ErrorResponse
// @Failure      503          {object}  httputil.ErrorResponse
// @Router       /notification-templates/{id}/preview [post]
// @Security     bearerAuth
func (h *Handler) previewTemplate(c *echo.Context) error {
	if h.templates == nil {
		return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{Error: "template engine not configured"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermNotificationTemplateView, "notification_template"); e != nil {
		return e
	}
	incID, _ := strconv.Atoi(c.QueryParam("incident_id"))
	if incID == 0 {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "incident_id required"})
	}
	ctx := c.Request().Context()
	tmpl, err := h.db.NotificationTemplate.Get(ctx, id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "template not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	inc, err := h.db.Incident.Get(ctx, incID)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "incident not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	rendered, err := h.templates.Render(ctx, tmpl.Name, string(tmpl.Channel), TemplateData{
		Incident: inc,
		Level:    inc.CurrentLevel,
	})
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"template_id":   tmpl.ID,
		"template_name": tmpl.Name,
		"title":         rendered.Title,
		"body":          rendered.Body,
	})
}

// parseTemplateActions 把请求中的 actions（map 列表）转 schema.TemplateAction。
func parseTemplateActions(actions []map[string]any) []schema.TemplateAction {
	out := make([]schema.TemplateAction, 0, len(actions))
	for _, a := range actions {
		t, _ := a["type"].(string)
		label, _ := a["label"].(string)
		if t == "" {
			continue
		}
		out = append(out, schema.TemplateAction{Type: t, Label: label})
	}
	return out
}
