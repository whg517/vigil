// handler.go AI 诊断 API（能力域 11）。
package ai

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// Handler AI 诊断 API。
type Handler struct {
	engine *DiagnoseEngine
}

// NewHandler 创建 AI handler。
func NewHandler(e *DiagnoseEngine) *Handler {
	return &Handler{engine: e}
}

// Register 挂载路由。
// POST /incidents/:id/diagnose     触发 AI 根因诊断
// GET  /incidents/:id/similar      查询相似历史事件
// POST /ai-insights/:id/resolve    人确认/拒绝 AI 建议（human-in-the-loop）
func (h *Handler) Register(g *echo.Group) {
	g.POST("/incidents/:id/diagnose", h.diagnose)
	g.GET("/incidents/:id/similar", h.similar)
	g.POST("/ai-insights/:id/resolve", h.resolve)
}

// diagnose 触发根因诊断。
func (h *Handler) diagnose(c echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	res, err := h.engine.Diagnose(c.Request().Context(), incID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if res == nil {
		return c.JSON(http.StatusOK, map[string]string{"status": "disabled", "message": "AI 诊断未启用（无 LLM）"})
	}
	return c.JSON(http.StatusCreated, res)
}

// similar 查询相似历史事件。
func (h *Handler) similar(c echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	similar, err := h.engine.FindSimilar(c.Request().Context(), incID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"similar": similar})
}

// resolveReq 确认/拒绝请求。
type resolveReq struct {
	Accepted bool `json:"accepted"`
}

// resolve 人确认/拒绝 AI 建议。
func (h *Handler) resolve(c echo.Context) error {
	insightID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req resolveReq
	_ = c.Bind(&req)
	if err := h.engine.ResolveInsight(c.Request().Context(), insightID, req.Accepted); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "resolved", "accepted": req.Accepted})
}
