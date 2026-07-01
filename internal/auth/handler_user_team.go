// handler_user_team.go 用户与团队管理 API（能力域 13 §用户/团队管理）。
//
// 此前 auth 包只有 roles/role-bindings handler，缺 users/teams。
// RBAC 角色绑定里 user_id/team_id 是裸 ID，无列表导致前端无法友好选择——本文件补齐。
//
// 权限点已存在：user.view/create/update/disable、team.view/create/update/delete（permission.go）。
package auth

import (
	"context"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// === User 管理 ===

// IMAccountBinder IM 账号绑定接口（QA 审计 C6）。
// im.Mapper 实现此接口；通过 SetIMAccountBinder 注入避免 auth→im 反向依赖（im 已 import auth）。
type IMAccountBinder interface {
	BindAccount(ctx context.Context, userID int, platform, unionID string) error
}

// IMAccountResolver IM 账号查询接口（列出用户已绑定的 IM 账号）。
type IMAccountResolver interface {
	ListBindings(ctx context.Context, userID int) ([]IMAccountInfo, error)
}

// IMAccountInfo IM 账号绑定信息（脱敏视图）。
type IMAccountInfo struct {
	Platform  string `json:"platform"`
	AccountID string `json:"account_id"`
}

// UserHandler 用户管理 API。
type UserHandler struct {
	db         *ent.Client
	imBinder   IMAccountBinder // 可选：IM 账号绑定（C6）
	imResolver IMAccountResolver
}

// NewUserHandler 创建用户 handler。
func NewUserHandler(db *ent.Client) *UserHandler {
	return &UserHandler{db: db}
}

// SetIMAccountBinder 注入 IM 账号绑定器（QA 审计 C6，main 装配时调用）。
func (h *UserHandler) SetIMAccountBinder(b IMAccountBinder) { h.imBinder = b }

// SetIMAccountResolver 注入 IM 账号查询器。
func (h *UserHandler) SetIMAccountResolver(r IMAccountResolver) { h.imResolver = r }

// Register 挂载用户管理路由。
func (h *UserHandler) Register(g *echo.Group) {
	g.GET("/users", h.listUsers)
	g.PATCH("/users/:id", h.updateUser)
	// QA 审计 C6：IM 账号绑定 API（原 Mapper.BindAccount 全仓 0 调用方，
	// 用户无法绑定 IM → ResolveUser 永远 ErrNotBound → 所有 IM 操作 403）。
	g.POST("/users/:id/im-accounts", h.bindIMAccount)
	g.GET("/users/:id/im-accounts", h.listIMAccounts)
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
		return errs.Internal(c, nil, err)
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
		return errs.FailNotFound(c, nil, err, "user")
	}
	return c.JSON(http.StatusOK, updated)
}

// bindIMAccountReq 绑定 IM 账号请求。
type bindIMAccountReq struct {
	Platform  string `json:"platform"`   // dingtalk | feishu | wecom
	AccountID string `json:"account_id"` // IM 平台 unionId
}

// bindIMAccount 给用户绑定一个 IM 平台账号（QA 审计 C6）。
// 权限点 user.im.bind 由 RouteGuard 在 wire.go 登记（POST /users/:id/im-accounts）。
//
// @Summary      绑定 IM 账号
// @Description  给指定用户绑定一个 IM 平台账号（platform + account_id），幂等。
// @Tags         user
// @Accept       json
// @Produce      json
// @Param        id    path      int                 true  "用户 ID"
// @Param        body  body      bindIMAccountReq    true  "IM 账号"
// @Success      201  {object}  bindIMAccountReq
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users/{id}/im-accounts [post]
func (h *UserHandler) bindIMAccount(c *echo.Context) error {
	if h.imBinder == nil {
		return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{Error: "im account binding not configured"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req bindIMAccountReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Platform == "" || req.AccountID == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "platform and account_id required"})
	}
	if err := h.imBinder.BindAccount(c.Request().Context(), id, req.Platform, req.AccountID); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, req)
}

// listIMAccounts 列出用户已绑定的 IM 账号。
//
// @Summary      列出 IM 账号
// @Tags         user
// @Produce      json
// @Param        id    path      int   true  "用户 ID"
// @Success      200  {array}   IMAccountInfo
// @Failure      500  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users/{id}/im-accounts [get]
func (h *UserHandler) listIMAccounts(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	// 优先用 resolver（独立表查询）；未注入则回退 User.im_accounts JSON 字段
	if h.imResolver != nil {
		accs, err := h.imResolver.ListBindings(c.Request().Context(), id)
		if err != nil {
			return errs.Internal(c, nil, err)
		}
		return c.JSON(http.StatusOK, accs)
	}
	// 回退：直接读 User.im_accounts JSON 字段
	u, err := h.db.User.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "user not found"})
	}
	out := make([]IMAccountInfo, 0, len(u.ImAccounts))
	for _, a := range u.ImAccounts {
		out = append(out, IMAccountInfo{Platform: a.Platform, AccountID: a.AccountID})
	}
	return c.JSON(http.StatusOK, out)
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
		return errs.Internal(c, nil, err)
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
		return errs.Internal(c, nil, err)
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
		return errs.FailNotFound(c, nil, err, "team")
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
