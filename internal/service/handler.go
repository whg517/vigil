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
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
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

// list 服务目录列表。
//
// @Summary      服务列表
// @Tags         service
// @Produce      json
// @Success      200  {array}   ent.Service
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services [get]
func (h *Handler) list(c *echo.Context) error {
	svcs, err := h.db.Service.Query().All(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, svcs)
}

// createReq 创建服务请求。
type createReq struct {
	Name               string            `json:"name"`
	Slug               string            `json:"slug"`
	Description        string            `json:"description"`
	Labels             map[string]string `json:"labels"`
	AutoCreateIncident *bool             `json:"auto_create_incident"`
	Status             string            `json:"status"` // active | disabled
	TeamID             int               `json:"team_id"`
	EscalationPolicyID int               `json:"escalation_policy_id"` // 可选，关联升级策略
}

// create 创建服务。
//
// @Summary      创建服务
// @Tags         service
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "服务创建参数"
// @Success      201   {object} ent.Service
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services [post]
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Slug == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and slug required"})
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
	if req.EscalationPolicyID > 0 {
		b.SetEscalationPolicyID(req.EscalationPolicyID)
	}
	s, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, s)
}

// get 服务详情。
//
// @Summary      服务详情
// @Tags         service
// @Produce      json
// @Param        id   path     int  true  "服务 ID"
// @Success      200  {object} ent.Service
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	s, err := h.db.Service.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, s)
}

// updateReq 更新服务请求（全部字段可选，部分更新）。
type updateReq struct {
	Name               *string            `json:"name"`
	Slug               *string            `json:"slug"`
	Description        *string            `json:"description"`
	Labels             *map[string]string `json:"labels"`
	AutoCreateIncident *bool              `json:"auto_create_incident"`
	Status             *string            `json:"status"`
	// EscalationPolicyID 关联升级策略。指针区分三种语义：
	//   nil  —— 不修改（请求未带该字段）
	//   0   —— 解除关联（显式清空）
	//   >0  —— 关联指定策略
	EscalationPolicyID *int `json:"escalation_policy_id"`
}

// update 更新服务。
//
// @Summary      更新服务
// @Tags         service
// @Accept       json
// @Produce      json
// @Param        id    path     int        true  "服务 ID"
// @Param        body  body     updateReq  true  "服务更新参数"
// @Success      200   {object} ent.Service
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
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
	// 升级策略关联：nil 不改，0 解绑，>0 关联。
	if req.EscalationPolicyID != nil {
		if *req.EscalationPolicyID > 0 {
			upd.SetEscalationPolicyID(*req.EscalationPolicyID)
		} else {
			upd.ClearEscalationPolicy()
		}
	}
	s, err := upd.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, s)
}

// delete 删除服务。
//
// @Summary      删除服务
// @Tags         service
// @Param        id   path  int  true  "服务 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.Service.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}
