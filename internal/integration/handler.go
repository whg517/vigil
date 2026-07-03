// Package integration 实现接入点管理 API（能力域 1 接入配置）。
//
// Integration 是告警源的接入配置（type/token/config/归属）。此前只有 schema 无 handler，
// 接入点只能靠 DB 手工建。本包补 list/get/create/update/delete。
//
// token 创建时自动生成（vgl_int_<rand>），webhook 用它做鉴权；list/get 不回显 token（Sensitive）。
package integration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行写操作，造成"报 403 却已落库"的越权。故 checkAccess 拒绝时返回
// 本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// tokenPrefix 接入 token 前缀（防与 API Key 混淆）。
const tokenPrefix = "vig_int_"

// Handler 接入点管理 API。
type Handler struct {
	db    *ent.Client
	authz *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建接入点 handler。
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

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 integration 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "integration", id)
	if err != nil {
		// errs.Internal 写完 500 返回 nil，必须换成非 nil 哨兵，否则调用方不会中止。
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		// 同理：errs.Forbidden 写完 403 返回 nil，返回哨兵让调用方 return 中止后续写操作。
		_ = errs.Forbidden(c, "")
		return errAccessDenied
	}
	return nil
}

// Register 挂载路由。
//
//	GET    /integrations
//	POST   /integrations
//	GET    /integrations/:id
//	PATCH  /integrations/:id
//	DELETE /integrations/:id
func (h *Handler) Register(g *echo.Group) {
	g.GET("/integrations", h.list)
	g.POST("/integrations", h.create)
	g.GET("/integrations/:id", h.get)
	g.PATCH("/integrations/:id", h.update)
	g.DELETE("/integrations/:id", h.delete)
}

// generateToken 生成接入 webhook 鉴权 token。
func generateToken() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return tokenPrefix + hex.EncodeToString(buf)
}

// createReq 创建接入点请求。
type createReq struct {
	Name      string         `json:"name"`
	Type      string         `json:"type"`   // webhook|email|prometheus|zabbix|grafana|cloud|api
	Config    map[string]any `json:"config"` // 类型相关配置（URL/过滤/限流等）
	TeamID    int            `json:"team_id"`
	ServiceID int            `json:"service_id"`
}

// createResp 创建响应（含一次性 token，后续不再回显）。
type createResp struct {
	*ent.Integration
	Token string `json:"token"` // ★ 明文 token，仅创建时返回一次
}

// list 接入点列表。
//
// @Summary      接入点列表
// @Tags         integration
// @Produce      json
// @Success      200  {array}   ent.Integration
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Integration.Query()
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
					return c.JSON(http.StatusOK, []*ent.Integration{})
				}
				q = q.Where(integration.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	ints, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, ints)
}

// create 创建接入点（自动生成 webhook 鉴权 token）。
//
// @Summary      创建接入点（返回 token 仅一次）
// @Tags         integration
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "接入点配置"
// @Success      201  {object} createResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations [post]
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Type == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and type required"})
	}

	b := h.db.Integration.Create().
		SetName(req.Name).
		SetType(integration.Type(req.Type)).
		SetToken(generateToken()).
		SetEnabled(true)
	if req.Config != nil {
		b.SetConfig(req.Config)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if req.ServiceID > 0 {
		b.SetServiceID(req.ServiceID)
	}
	integ, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "integration", "integration already exists")
	}
	return c.JSON(http.StatusCreated, createResp{Integration: integ, Token: integ.Token})
}

// get 接入点详情。
//
// @Summary      接入点详情
// @Tags         integration
// @Produce      json
// @Param        id   path      int  true  "接入点 ID"
// @Success      200  {object} ent.Integration
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
	}
	integ, err := h.db.Integration.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "integration not found"})
	}
	return c.JSON(http.StatusOK, integ)
}

// updateReq 更新接入点请求（全指针，支持部分更新）。
type updateReq struct {
	Name    *string `json:"name"`
	Enabled *bool   `json:"enabled"`
}

// update 更新接入点（名称/启停）。
//
// @Summary      更新接入点
// @Tags         integration
// @Accept       json
// @Produce      json
// @Param        id    path      int        true  "接入点 ID"
// @Param        body  body      updateReq  true  "更新字段"
// @Success      200  {object} ent.Integration
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.Integration.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Enabled != nil {
		u.SetEnabled(*req.Enabled)
	}
	integ, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "integration")
	}
	return c.JSON(http.StatusOK, integ)
}

// delete 删除接入点。
//
// @Summary      删除接入点
// @Tags         integration
// @Param        id   path      int  true  "接入点 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
	}
	if err := h.db.Integration.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
