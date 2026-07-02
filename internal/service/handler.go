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
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 服务目录 API。
type Handler struct {
	db    *ent.Client
	authz *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建服务目录 handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
}

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 service 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "service", id)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if !allowed {
		return errs.Forbidden(c, "")
	}
	return nil
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
	ctx := c.Request().Context()
	q := h.db.Service.Query()
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// org 级用户（orgWide）全可见；team 级用户仅可见 binding 的 team；无 binding 返回空。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.Service{})
				}
				q = q.Where(entservice.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	svcs, err := q.All(ctx)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	if err := h.db.Service.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}
