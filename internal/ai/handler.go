// handler.go AI 诊断 API（能力域 11）。
package ai

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
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
// GET  /incidents/:id/similar-postmortems  查询相似已发布复盘（知识沉淀 M12.6）
// POST /ai-insights/:id/resolve    人确认/拒绝 AI 建议（human-in-the-loop）
func (h *Handler) Register(g *echo.Group) {
	g.POST("/incidents/:id/diagnose", h.diagnose)
	g.GET("/incidents/:id/similar", h.similar)
	g.GET("/incidents/:id/similar-postmortems", h.similarPostmortems)
	g.POST("/ai-insights/:id/resolve", h.resolve)
}

// DiagnoseIncident 触发根因诊断。
//
// @Summary      Diagnose incident (AI)
// @Description  触发 LLM 根因诊断；若未启用 LLM 返回 200 与 disabled 状态。
// @Tags         ai
// @Produce      json
// @Param        id   path      int  true  "Incident ID"
// @Success      200  {object}  map[string]string  "AI 诊断未启用"
// @Success      201  {object}  ai.DiagnoseResult
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/diagnose [post]
// @Security     bearerAuth
func (h *Handler) diagnose(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	res, err := h.engine.Diagnose(c.Request().Context(), incID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	if res == nil {
		return c.JSON(http.StatusOK, map[string]string{"status": "disabled", "message": "AI 诊断未启用（无 LLM）"})
	}
	return c.JSON(http.StatusCreated, res)
}

// FindSimilarIncidents 查询相似历史事件。
//
// @Summary      Find similar incidents
// @Description  按向量/文本相似度查询与给定 incident 相似的历史事件。
// @Tags         ai
// @Produce      json
// @Param        id     path   int  true   "Incident ID"
// @Param        limit  query  int  false  "返回条数上限"
// @Success      200    {object}  map[string]any  "{similar: []*ent.Incident}"
// @Failure      400    {object}  httputil.ErrorResponse
// @Failure      500    {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/similar [get]
// @Security     bearerAuth
func (h *Handler) similar(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	similar, err := h.engine.FindSimilar(c.Request().Context(), incID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"similar": similar})
}

// similarPostmortems 查询相似的已发布复盘（知识沉淀 M12.6）。
// "上次类似故障是怎么处理的"——published 复盘反哺新事件诊断。
func (h *Handler) similarPostmortems(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	pms, err := h.engine.FindSimilarPostmortems(c.Request().Context(), incID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"similar_postmortems": pms})
}

// resolveReq 确认/拒绝请求。
type resolveReq struct {
	Accepted bool `json:"accepted"`
}

// ResolveAIInsight 人确认/拒绝 AI 建议。
//
// @Summary      Resolve AI insight
// @Description  人确认/拒绝 AI 建议（human-in-the-loop）。
// @Tags         ai
// @Accept       json
// @Produce      json
// @Param        id       path      int          true  "AI Insight ID"
// @Param        request  body      resolveReq   true  "accepted=true 接受，false 拒绝"
// @Success      200      {object}  map[string]any
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /ai-insights/{id}/resolve [post]
// @Security     bearerAuth
func (h *Handler) resolve(c *echo.Context) error {
	insightID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req resolveReq
	_ = c.Bind(&req)
	if err := h.engine.ResolveInsight(c.Request().Context(), insightID, req.Accepted); err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "resolved", "accepted": req.Accepted})
}
