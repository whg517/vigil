// Package service 实现服务目录 API（能力域 4/13 服务管理）。
//
// 对应 data-model.md §3.2 Service。Service 是路由的锚点、软隔离的核心载体。
// 此前 Service 仅有 ent schema 无 HTTP handler，本包补 list/get/create/update/delete。
//
// 权限点 service.* 由调用方在装配时按角色授权（与 auth.Handler 一致）。
package service

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entservice "github.com/kevin/vigil/ent/service"

	"github.com/labstack/echo/v4"
)

// Handler 服务目录 API。
type Handler struct {
	db *ent.Client
}

// NewHandler 创建服务目录 handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
}

// Register 挂载路由。
//
//	GET    /services
//	POST   /services
//	GET    /services/:id
//	PATCH  /services/:id
//	DELETE /services/:id
func (h *Handler) Register(g *echo.Group) {
	g.GET("/services", h.list)
	g.POST("/services", h.create)
	g.GET("/services/:id", h.get)
	g.PATCH("/services/:id", h.update)
	g.DELETE("/services/:id", h.delete)
}

func (h *Handler) list(c echo.Context) error {
	svcs, err := h.db.Service.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, svcs)
}

type createReq struct {
	Name               string            `json:"name"`
	Slug               string            `json:"slug"`
	Description        string            `json:"description"`
	Labels             map[string]string `json:"labels"`
	AutoCreateIncident *bool             `json:"auto_create_incident"`
	Status             string            `json:"status"` // active | disabled
	TeamID             int               `json:"team_id"`
}

func (h *Handler) create(c echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" || req.Slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name and slug required"})
	}
	b := h.db.Service.Create().
		SetName(req.Name).
		SetSlug(req.Slug)
	if req.Description != "" {
		b.SetDescription(req.Description)
	}
	if req.Labels != nil {
		b.SetLabels(req.Labels)
	}
	if req.AutoCreateIncident != nil {
		b.SetAutoCreateIncident(*req.AutoCreateIncident)
	}
	if req.Status != "" {
		b.SetStatus(entservice.Status(req.Status))
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	s, err := b.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, s)
}

func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	s, err := h.db.Service.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, s)
}

type updateReq struct {
	Name               *string            `json:"name"`
	Slug               *string            `json:"slug"`
	Description        *string            `json:"description"`
	Labels             *map[string]string `json:"labels"`
	AutoCreateIncident *bool              `json:"auto_create_incident"`
	Status             *string            `json:"status"`
}

func (h *Handler) update(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	upd := h.db.Service.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Slug != nil {
		upd.SetSlug(*req.Slug)
	}
	if req.Description != nil {
		upd.SetDescription(*req.Description)
	}
	if req.Labels != nil {
		upd.SetLabels(*req.Labels)
	}
	if req.AutoCreateIncident != nil {
		upd.SetAutoCreateIncident(*req.AutoCreateIncident)
	}
	if req.Status != nil {
		upd.SetStatus(entservice.Status(*req.Status))
	}
	s, err := upd.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, s)
}

func (h *Handler) delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.db.Service.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
