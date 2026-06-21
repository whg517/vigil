// handler.go Runbook API（CRUD + 触发执行）。
package runbook

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v4"
)

// Handler Runbook API。
type Handler struct {
	db     *ent.Client
	engine *Engine
}

// NewHandler 创建 Runbook handler。
func NewHandler(db *ent.Client, e *Engine) *Handler {
	return &Handler{db: db, engine: e}
}

// Register 挂载路由（鉴权中间件由装配方按需添加）。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/runbooks", h.list)
	g.POST("/runbooks", h.create)
	g.GET("/runbooks/:id", h.get)
	g.DELETE("/runbooks/:id", h.delete)
	g.POST("/runbooks/:id/execute", h.execute)
}

// ListRunbooks 列出全部 Runbook。
//
// @Summary      List runbooks
// @Description  返回全部 Runbook（无分页）。
// @Tags         runbook
// @Produce      json
// @Success      200  {array}  ent.Runbook
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /runbooks [get]
// @Security     bearerAuth
func (h *Handler) list(c echo.Context) error {
	rbs, err := h.db.Runbook.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, rbs)
}

type createReq struct {
	Name            string               `json:"name"`
	Type            string               `json:"type"` // document | executable
	ContentMarkdown string               `json:"content_markdown"`
	Trigger         map[string]any       `json:"trigger"`
	Steps           []schema.RunbookStep `json:"steps"`
}

// CreateRunbook 创建 Runbook。
//
// @Summary      Create runbook
// @Description  新建 Runbook（文档型或可执行型，含 trigger 与 steps）。
// @Tags         runbook
// @Accept       json
// @Produce      json
// @Param        request  body      createReq  true  "Runbook 定义"
// @Success      201      {object}  ent.Runbook
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /runbooks [post]
// @Security     bearerAuth
func (h *Handler) create(c echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	rb := h.db.Runbook.Create().SetName(req.Name).SetType(entrunbook.Type(req.Type))
	if req.ContentMarkdown != "" {
		rb.SetContentMarkdown(req.ContentMarkdown)
	}
	if req.Trigger != nil {
		rb.SetTrigger(req.Trigger)
	}
	if len(req.Steps) > 0 {
		rb.SetSteps(req.Steps)
	}
	saved, err := rb.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusCreated, saved)
}

// GetRunbook 获取单个 Runbook。
//
// @Summary      Get runbook
// @Description  按 ID 取得 Runbook。
// @Tags         runbook
// @Produce      json
// @Param        id   path      int  true  "Runbook ID"
// @Success      200  {object}  ent.Runbook
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Router       /runbooks/{id} [get]
// @Security     bearerAuth
func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	rb, err := h.db.Runbook.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	return c.JSON(http.StatusOK, rb)
}

// DeleteRunbook 删除 Runbook。
//
// @Summary      Delete runbook
// @Description  按 ID 删除 Runbook。
// @Tags         runbook
// @Param        id   path  int  true  "Runbook ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /runbooks/{id} [delete]
// @Security     bearerAuth
func (h *Handler) delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.Runbook.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// executeReq 触发执行请求。
type executeReq struct {
	IncidentID int  `json:"incident_id"`
	Approved   bool `json:"approved"` // 写动作是否已确认（human-in-the-loop）
}

// ExecuteRunbook 触发执行 Runbook。
//
// @Summary      Execute runbook
// @Description  按 incident 触发 Runbook 执行（approved=false 时跳过写动作，human-in-the-loop）。
// @Tags         runbook
// @Accept       json
// @Produce      json
// @Param        id       path      int          true  "Runbook ID"
// @Param        request  body      executeReq   true  "执行参数（incident_id + approved）"
// @Success      200      {object}  runbook.ExecuteResult
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /runbooks/{id}/execute [post]
// @Security     bearerAuth
func (h *Handler) execute(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req executeReq
	_ = c.Bind(&req) // approved 可缺省（默认 false，写动作会被跳过）

	res, err := h.engine.Execute(c.Request().Context(), id, req.IncidentID, req.Approved)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}
