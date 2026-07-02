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
	"net/http"
	"strconv"

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
		return errs.Internal(c, nil, err)
	}
	if !allowed {
		return errs.Forbidden(c, "")
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
		return errs.Internal(c, nil, err)
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
// @Description  返回全部 SuppressionRule（无分页）。
// @Tags         suppression
// @Produce      json
// @Success      200  {array}  ent.SuppressionRule
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /suppression-rules [get]
// @Security     bearerAuth
func (h *Handler) listSuppressions(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.SuppressionRule.Query()
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
	b := h.db.SuppressionRule.Create().
		SetName(req.Name).
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
	r, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
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
	MatchLabels      *map[string]string `json:"match_labels"`
	TimeWindow       *map[string]any    `json:"time_window"`
	SeverityFilter   *[]string          `json:"severity_filter"`
	Action           *string            `json:"action"`
	ReduceTo         *string            `json:"reduce_to"`
	PreserveCritical *bool              `json:"preserve_critical"`
	Enabled          *bool              `json:"enabled"`
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
	upd := h.db.SuppressionRule.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
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
		return errs.Internal(c, nil, err)
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
