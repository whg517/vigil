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
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v4"
)

// tokenPrefix 接入 token 前缀（防与 API Key 混淆）。
const tokenPrefix = "vig_int_"

// Handler 接入点管理 API。
type Handler struct {
	db *ent.Client
}

// NewHandler 创建接入点 handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
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
func (h *Handler) list(c echo.Context) error {
	ints, err := h.db.Integration.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
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
func (h *Handler) create(c echo.Context) error {
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
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
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
func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
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
func (h *Handler) update(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
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
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: err.Error()})
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
func (h *Handler) delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.Integration.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
