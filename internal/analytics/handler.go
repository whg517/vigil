// handler.go 报表 API（能力域 15）。
package analytics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
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
func parseRange(c echo.Context) Range {
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

func (h *Handler) dashboard(c echo.Context) error {
	days, _ := strconv.Atoi(c.QueryParam("days"))
	d, err := h.engine.Dashboard(c.Request().Context(), days)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, d)
}

func (h *Handler) alerts(c echo.Context) error {
	m, err := h.engine.AlertMetrics(c.Request().Context(), parseRange(c))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) incidents(c echo.Context) error {
	m, err := h.engine.IncidentMetrics(c.Request().Context(), parseRange(c))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) teamLoad(c echo.Context) error {
	m, err := h.engine.TeamLoad(c.Request().Context(), parseRange(c))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) postmortems(c echo.Context) error {
	m, err := h.engine.PostmortemMetrics(c.Request().Context(), parseRange(c))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handler) trend(c echo.Context) error {
	days, _ := strconv.Atoi(c.QueryParam("days"))
	points, err := h.engine.Trend(c.Request().Context(), days, parseRange(c))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"days": points})
}
