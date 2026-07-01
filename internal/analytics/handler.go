// handler.go 报表 API（能力域 15）。
package analytics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/internal/errs"

	"github.com/labstack/echo/v5"
)

// Handler 报表 API。
type Handler struct {
	engine *Engine
}

// NewHandler 创建报表 handler。
func NewHandler(e *Engine) *Handler {
	return &Handler{engine: e}
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
	days, _ := strconv.Atoi(c.QueryParam("days"))
	d, err := h.engine.Dashboard(c.Request().Context(), days)
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
	m, err := h.engine.AlertMetrics(c.Request().Context(), parseRange(c))
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
	m, err := h.engine.IncidentMetrics(c.Request().Context(), parseRange(c))
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
	m, err := h.engine.TeamLoad(c.Request().Context(), parseRange(c))
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
	m, err := h.engine.PostmortemMetrics(c.Request().Context(), parseRange(c))
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
	days, _ := strconv.Atoi(c.QueryParam("days"))
	points, err := h.engine.Trend(c.Request().Context(), days, parseRange(c))
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"days": points})
}
