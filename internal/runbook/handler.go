// handler.go Runbook API（CRUD + 触发执行）。
package runbook

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"

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

func (h *Handler) list(c echo.Context) error {
	rbs, err := h.db.Runbook.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, rbs)
}

type createReq struct {
	Name            string                 `json:"name"`
	Type            string                 `json:"type"` // document | executable
	ContentMarkdown string                 `json:"content_markdown"`
	Trigger         map[string]any         `json:"trigger"`
	Steps           []schema.RunbookStep   `json:"steps"`
}

func (h *Handler) create(c echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, saved)
}

func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	rb, err := h.db.Runbook.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, rb)
}

func (h *Handler) delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.db.Runbook.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// executeReq 触发执行请求。
type executeReq struct {
	IncidentID int  `json:"incident_id"`
	Approved   bool `json:"approved"` // 写动作是否已确认（human-in-the-loop）
}

// execute 触发执行 Runbook。
func (h *Handler) execute(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req executeReq
	_ = c.Bind(&req) // approved 可缺省（默认 false，写动作会被跳过）

	res, err := h.engine.Execute(c.Request().Context(), id, req.IncidentID, req.Approved)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}
