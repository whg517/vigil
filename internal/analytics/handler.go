// handler.go 报表 API（能力域 15）。
package analytics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"

	"github.com/labstack/echo/v5"
)

// Handler 报表 API。
type Handler struct {
	engine *Engine
	authz  *auth.Authorizer // 团队数据隔离（S14）：解析当前用户可见 team 集合
}

// NewHandler 创建报表 handler。
func NewHandler(e *Engine) *Handler {
	return &Handler{engine: e}
}

// SetAuthorizer 注入鉴权器，启用团队 scope 数据隔离（S14）。
// 未注入时退化为看全组织（AllTeams），仅用于测试桩/未装配场景。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) *Handler {
	h.authz = a
	return h
}

// resolveScope 解析当前请求用户的可见团队范围（S14 团队软隔离）。
//
// 报表是团队数据归属边界：team 级 Leader 只应看到本团队指标，org 级角色看全组织。
// 复用 auth.VisibleTeamIDs 的语义（org 级 binding → orgWide；否则仅可见 team）。
//
// 降级：未注入 authz 或未取得用户身份时看全组织——路由已挂 analytics.view 权限点
// 拦截未授权用户，此处不再重复鉴权，仅决定「看多大范围」。
func (h *Handler) resolveScope(ctx context.Context) (Scope, error) {
	if h.authz == nil {
		return AllTeams(), nil
	}
	uid, ok := auth.UserIDFromContext(ctx)
	if !ok || uid <= 0 {
		return AllTeams(), nil
	}
	teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
	if err != nil {
		return Scope{}, err
	}
	return Scope{OrgWide: orgWide, TeamIDs: teamIDs}, nil
}

// Register 挂载路由。
// GET /analytics/dashboard        仪表盘汇总（?days=7）
// GET /analytics/alerts           告警度量
// GET /analytics/incidents        事件度量
// GET /analytics/team-load        团队负载
// GET /analytics/postmortems      复盘度量
// GET /analytics/trend            趋势（?days=7）
func (h *Handler) Register(g *echo.Group) {
	g.GET("/analytics/dashboard", h.dashboard)
	g.GET("/analytics/alerts", h.alerts)
	g.GET("/analytics/incidents", h.incidents)
	g.GET("/analytics/team-load", h.teamLoad)
	g.GET("/analytics/postmortems", h.postmortems)
	g.GET("/analytics/trend", h.trend)
}

// parseRange 从 query 解析时间范围。start/end 为 RFC3339。
func parseRange(c *echo.Context) Range {
	var r Range
	if s := c.QueryParam("start"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			r.Start = t
		}
	}
	if e := c.QueryParam("end"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			r.End = t
		}
	}
	return r
}

// GetDashboard 仪表盘汇总。
//
// @Summary      Get analytics dashboard
// @Description  返回近 N 天的仪表盘汇总（默认 N=7）。
// @Tags         analytics
// @Produce      json
// @Param        days  query  int  false  "时间窗（天）"
// @Success      200  {object}  analytics.Dashboard
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/dashboard [get]
// @Security     bearerAuth
func (h *Handler) dashboard(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	days, _ := strconv.Atoi(c.QueryParam("days"))
	d, err := h.engine.Dashboard(ctx, days, scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, d)
}

// GetAlertMetrics 告警度量。
//
// @Summary      Get alert metrics
// @Description  按 start/end（RFC3339）时间范围返回告警度量。
// @Tags         analytics
// @Produce      json
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {object}  analytics.AlertMetrics
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/alerts [get]
// @Security     bearerAuth
func (h *Handler) alerts(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.AlertMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, m)
}

// GetIncidentMetrics 事件度量。
//
// @Summary      Get incident metrics
// @Description  按 start/end（RFC3339）时间范围返回事件度量。
// @Tags         analytics
// @Produce      json
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {object}  analytics.IncidentMetrics
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/incidents [get]
// @Security     bearerAuth
func (h *Handler) incidents(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.IncidentMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, m)
}

// GetTeamLoad 团队负载。
//
// @Summary      Get team load
// @Description  按 start/end（RFC3339）时间范围返回各团队负载度量。
// @Tags         analytics
// @Produce      json
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {array}   analytics.TeamLoad
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/team-load [get]
// @Security     bearerAuth
func (h *Handler) teamLoad(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.TeamLoad(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, m)
}

// GetPostmortemMetrics 复盘度量。
//
// @Summary      Get postmortem metrics
// @Description  按 start/end（RFC3339）时间范围返回复盘度量。
// @Tags         analytics
// @Produce      json
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {object}  analytics.PostmortemMetrics
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/postmortems [get]
// @Security     bearerAuth
func (h *Handler) postmortems(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.PostmortemMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, m)
}

// GetTrend 趋势。
//
// @Summary      Get analytics trend
// @Description  按 days/时间范围返回趋势点序列。
// @Tags         analytics
// @Produce      json
// @Param        days   query  int     false  "时间窗（天）"
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {array}   analytics.TrendPoint
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/trend [get]
// @Security     bearerAuth
func (h *Handler) trend(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	days, _ := strconv.Atoi(c.QueryParam("days"))
	points, err := h.engine.Trend(ctx, days, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"days": points})
}
