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
	"github.com/kevin/vigil/ent/notificationtemplate"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/suppressionrule"

	"github.com/labstack/echo/v4"
)

// Handler 通知配置 API（规则 + 抑制 + 模板）。
type Handler struct {
	db         *ent.Client
	notifier   *Notifier // 用于 test 端点 dry-run
	aggregator *Aggregator
	templates  *TemplateEngine // 用于 preview 端点
}

// NewHandler 创建通知配置 handler。notifier/aggregator/templates 可为 nil（对应端点降级）。
func NewHandler(db *ent.Client, notifier *Notifier, aggregator *Aggregator) *Handler {
	return &Handler{db: db, notifier: notifier, aggregator: aggregator}
}

// SetTemplateEngine 注入模板引擎（template CRUD + preview 需要）。
func (h *Handler) SetTemplateEngine(e *TemplateEngine) {
	h.templates = e
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

func (h *Handler) listRules(c echo.Context) error {
	rules, err := h.db.NotificationRule.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, rules)
}

type createRuleReq struct {
	Name        string         `json:"name"`
	Condition   map[string]any `json:"condition"`
	Channels    []string       `json:"channels"`
	TemplateID  string         `json:"template_id"`
	QuietHours  map[string]any `json:"quiet_hours"`
	TeamID      int            `json:"team_id"`
	Enabled     *bool          `json:"enabled"`
}

func (h *Handler) createRule(c echo.Context) error {
	var req createRuleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, r)
}

func (h *Handler) getRule(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	r, err := h.db.NotificationRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) updateRule(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req updateRuleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, r)
}

func (h *Handler) deleteRule(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.db.NotificationRule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// testRule dry-run：给定 incident_id，评估该规则是否对其静默。
// 返回 {quiet_hours_suppress: bool, quiet_hours_config: {...}, channels: [...]}。
func (h *Handler) testRule(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	incIDStr := c.QueryParam("incident_id")
	incID, _ := strconv.Atoi(incIDStr)
	r, err := h.db.NotificationRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "rule not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) listSuppressions(c echo.Context) error {
	rules, err := h.db.SuppressionRule.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) createSuppression(c echo.Context) error {
	var req createSuppressionReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, r)
}

func (h *Handler) getSuppression(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	r, err := h.db.SuppressionRule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) updateSuppression(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req updateSuppressionReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, r)
}

func (h *Handler) deleteSuppression(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.db.SuppressionRule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// ===== NotificationTemplate =====

func (h *Handler) listTemplates(c echo.Context) error {
	templates, err := h.db.NotificationTemplate.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) createTemplate(c echo.Context) error {
	var req createTemplateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" || req.TitleTemplate == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name and title_template required"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.JSON(http.StatusCreated, t)
}

func (h *Handler) getTemplate(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	t, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

func (h *Handler) updateTemplate(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	// 内置模板不可改（只能由 seed 更新）
	existing, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if existing.Builtin {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "builtin template cannot be modified"})
	}
	var req updateTemplateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.JSON(http.StatusOK, t)
}

func (h *Handler) deleteTemplate(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	existing, err := h.db.NotificationTemplate.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if existing.Builtin {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "builtin template cannot be deleted"})
	}
	if err := h.db.NotificationTemplate.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if h.templates != nil {
		h.templates.InvalidateCache()
	}
	return c.NoContent(http.StatusNoContent)
}

// previewTemplate 传 incident_id，返回该模板渲染后的 title/body。
// 所见即所得，用于模板编辑器预览。
func (h *Handler) previewTemplate(c echo.Context) error {
	if h.templates == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "template engine not configured"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	incID, _ := strconv.Atoi(c.QueryParam("incident_id"))
	if incID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "incident_id required"})
	}
	ctx := c.Request().Context()
	tmpl, err := h.db.NotificationTemplate.Get(ctx, id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "template not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	inc, err := h.db.Incident.Get(ctx, incID)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "incident not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	rendered, err := h.templates.Render(ctx, tmpl.Name, string(tmpl.Channel), TemplateData{
		Incident: inc,
		Level:    inc.CurrentLevel,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
