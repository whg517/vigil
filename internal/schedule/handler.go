// handler.go 排班 API handler。
package schedule

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"

	"github.com/labstack/echo/v4"
)

// Handler 排班 API。
type Handler struct {
	engine *Engine
}

// NewHandler 创建排班 handler。
func NewHandler(e *Engine) *Handler {
	return &Handler{engine: e}
}

// Register 把排班路由挂到 group。
// GET /schedules/:id/oncall  —— 查询某时刻在班人
// GET /schedules/:id/preview —— 预览未来 N 天排班
func (h *Handler) Register(g *echo.Group) {
	g.GET("/schedules/:id/oncall", h.oncall)
	g.GET("/schedules/:id/preview", h.preview)
}

// oncall 查询某 Schedule 在指定时刻的在班人。
// query: time（ISO8601，可选，默认 now）
func (h *Handler) oncall(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}

	var at time.Time
	if t := c.QueryParam("time"); t != "" {
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid time (use RFC3339)"})
		}
		at = parsed
	}

	res, err := h.engine.Oncall(c.Request().Context(), id, at)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "schedule not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// preview 预览未来 N 天排班。
// query: days（默认 14）
func (h *Handler) preview(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	days := 14
	if d := c.QueryParam("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 90 {
			days = parsed
		}
	}

	res, err := h.engine.Preview(c.Request().Context(), id, days)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "schedule not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"schedule_id": id, "days": res})
}
