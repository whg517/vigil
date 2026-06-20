// handler.go Postmortem/ActionItem API（能力域 12）。
package postmortem

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/actionitem"
	"github.com/kevin/vigil/ent/postmortem"

	"github.com/labstack/echo/v4"
)

// Handler 复盘 API。
type Handler struct {
	db     *ent.Client
	engine *Engine
}

// NewHandler 创建复盘 handler。
func NewHandler(db *ent.Client, e *Engine) *Handler {
	return &Handler{db: db, engine: e}
}

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/postmortems", h.list)
	g.GET("/postmortems/:id", h.get)
	g.POST("/incidents/:id/postmortem/draft", h.generateDraft) // 为事件生成草稿
	g.PATCH("/postmortems/:id/transition", h.transition)        // 状态流转
	// ActionItem
	g.POST("/postmortems/:id/action-items", h.addActionItem)
	g.PATCH("/action-items/:id", h.updateActionItem)
}

func (h *Handler) list(c echo.Context) error {
	pms, err := h.db.Postmortem.Query().WithIncident().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, pms)
}

func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	pm, err := h.db.Postmortem.Query().
		Where(postmortem.IDEQ(id)).
		WithIncident().
		WithActionItems().
		Only(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, pm)
}

// generateDraft 为事件生成复盘草稿（AI + 时间线）。
func (h *Handler) generateDraft(c echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid incident id"})
	}
	pm, err := h.engine.GenerateDraft(c.Request().Context(), incID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, pm)
}

// transitionReq 状态流转请求。
type transitionReq struct {
	Status string `json:"status"` // in_review | published | archived
}

func (h *Handler) transition(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req transitionReq
	if err := c.Bind(&req); err != nil || req.Status == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "status required"})
	}
	pm, err := h.engine.Transition(c.Request().Context(), id, postmortem.Status(req.Status))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, pm)
}

// addActionItemReq 添加改进项。
type addActionItemReq struct {
	Description string `json:"description"`
	OwnerID     string `json:"owner_id"`
	TrackerURL  string `json:"tracker_url"`
}

func (h *Handler) addActionItem(c echo.Context) error {
	pmID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req addActionItemReq
	if err := c.Bind(&req); err != nil || req.Description == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "description required"})
	}
	ai, err := h.db.ActionItem.Create().
		SetDescription(req.Description).
		SetOwnerID(req.OwnerID).
		SetTrackerURL(req.TrackerURL).
		SetPostmortemID(pmID).
		Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, ai)
}

// updateActionItemReq 更新改进项（状态/负责人/工单）。
type updateActionItemReq struct {
	Status      *string `json:"status"`       // open | in_progress | done
	OwnerID     *string `json:"owner_id"`
	TrackerURL  *string `json:"tracker_url"`
}

func (h *Handler) updateActionItem(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req updateActionItemReq
	_ = c.Bind(&req)

	update := h.db.ActionItem.UpdateOneID(id)
	if req.Status != nil {
		update.SetStatus(actionitem.Status(*req.Status))
	}
	if req.OwnerID != nil {
		update.SetOwnerID(*req.OwnerID)
	}
	if req.TrackerURL != nil {
		update.SetTrackerURL(*req.TrackerURL)
	}
	ai, err := update.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, ai)
}
