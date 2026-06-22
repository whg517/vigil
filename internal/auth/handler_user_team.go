// handler_user_team.go 用户与团队管理 API（能力域 13 §用户/团队管理）。
//
// 此前 auth 包只有 roles/role-bindings handler，缺 users/teams。
// RBAC 角色绑定里 user_id/team_id 是裸 ID，无列表导致前端无法友好选择——本文件补齐。
//
// 权限点已存在：user.view/create/update/disable、team.view/create/update/delete（permission.go）。
package auth

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// === User 管理 ===

// UserHandler 用户管理 API。
type UserHandler struct {
	db *ent.Client
}

// NewUserHandler 创建用户 handler。
func NewUserHandler(db *ent.Client) *UserHandler {
	return &UserHandler{db: db}
}

// Register 挂载用户管理路由。
func (h *UserHandler) Register(g *echo.Group) {
	g.GET("/users", h.listUsers)
	g.PATCH("/users/:id", h.updateUser)
}

// listUsers 用户列表（不回显 password_hash，ent Sensitive 自动脱敏）。
//
// @Summary      用户列表
// @Tags         user
// @Produce      json
// @Success      200  {array}   ent.User
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users [get]
func (h *UserHandler) listUsers(c *echo.Context) error {
	users, err := h.db.User.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, users)
}

// updateUserReq 更新用户请求（name/status/timezone，不改密码）。
type updateUserReq struct {
	Name     *string `json:"name"`
	Status   *string `json:"status"` // active|disabled
	Timezone *string `json:"timezone"`
}

// updateUser 更新用户信息（启停/改名，不改密码——密码改走独立流程）。
//
// @Summary      更新用户
// @Tags         user
// @Accept       json
// @Produce      json
// @Param        id    path      int             true  "用户 ID"
// @Param        body  body      updateUserReq   true  "更新字段"
// @Success      200  {object} ent.User
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users/{id} [patch]
func (h *UserHandler) updateUser(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req updateUserReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.User.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Status != nil {
		u.SetStatus(user.Status(*req.Status))
	}
	if req.Timezone != nil {
		u.SetTimezone(*req.Timezone)
	}
	updated, err := u.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

// === Team 管理 ===

// TeamHandler 团队管理 API。
type TeamHandler struct {
	db *ent.Client
}

// NewTeamHandler 创建团队 handler。
func NewTeamHandler(db *ent.Client) *TeamHandler {
	return &TeamHandler{db: db}
}

// Register 挂载团队管理路由。
func (h *TeamHandler) Register(g *echo.Group) {
	g.GET("/teams", h.listTeams)
	g.POST("/teams", h.createTeam)
	g.PATCH("/teams/:id", h.updateTeam)
	g.DELETE("/teams/:id", h.deleteTeam)
}

// createTeamReq 创建团队请求。
type createTeamReq struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

// listTeams 团队列表。
//
// @Summary      团队列表
// @Tags         team
// @Produce      json
// @Success      200  {array}   ent.Team
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams [get]
func (h *TeamHandler) listTeams(c *echo.Context) error {
	teams, err := h.db.Team.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, teams)
}

// createTeam 创建团队。
//
// @Summary      创建团队
// @Tags         team
// @Accept       json
// @Produce      json
// @Param        body  body     createTeamReq  true  "团队配置"
// @Success      201  {object} ent.Team
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams [post]
func (h *TeamHandler) createTeam(c *echo.Context) error {
	var req createTeamReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Slug == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and slug required"})
	}
	b := h.db.Team.Create().SetName(req.Name).SetSlug(req.Slug)
	if req.Description != "" {
		b.SetDescription(req.Description)
	}
	t, err := b.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusCreated, t)
}

// updateTeamReq 更新团队请求。
type updateTeamReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// updateTeam 更新团队。
//
// @Summary      更新团队
// @Tags         team
// @Accept       json
// @Produce      json
// @Param        id    path      int             true  "团队 ID"
// @Param        body  body      updateTeamReq   true  "更新字段"
// @Success      200  {object} ent.Team
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams/{id} [patch]
func (h *TeamHandler) updateTeam(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req updateTeamReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.Team.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Description != nil {
		u.SetDescription(*req.Description)
	}
	t, err := u.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, t)
}

// deleteTeam 删除团队。
//
// @Summary      删除团队
// @Tags         team
// @Param        id   path      int  true  "团队 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams/{id} [delete]
func (h *TeamHandler) deleteTeam(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.Team.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
