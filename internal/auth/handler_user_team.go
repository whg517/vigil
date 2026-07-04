// handler_user_team.go 用户与团队管理 API（能力域 13 §用户/团队管理）。
//
// 此前 auth 包只有 roles/role-bindings handler，缺 users/teams。
// RBAC 角色绑定里 user_id/team_id 是裸 ID，无列表导致前端无法友好选择——本文件补齐。
//
// 权限点已存在：user.view/create/update/disable、team.view/create/update/delete（permission.go）。
package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/team"
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
	authz      *Authorizer    // 可选：细分「停用」为独立权限点（user.disable）用
	audit      *AuditRecorder // 可选：用户启停留痕（C21，nil 时跳过）
}

// NewUserHandler 创建用户 handler。
func NewUserHandler(db *ent.Client) *UserHandler {
	return &UserHandler{db: db}
}

// SetAuthorizer 注入鉴权器（审计 S2）。
// PATCH /users/:id 的路由级守卫只校验 user.update；当请求改动 status（启停）时，
// 停用是更敏感的动作，需额外持有 user.disable。注入后 updateUser 做此细分校验；
// 未注入（如测试）则退化为仅 user.update 门禁。
func (h *UserHandler) SetAuthorizer(a *Authorizer) { h.authz = a }

// SetAuditRecorder 注入审计记录器（C21：用户启停留痕，main 装配时调用）。
func (h *UserHandler) SetAuditRecorder(r *AuditRecorder) { h.audit = r }

// SetIMAccountBinder 注入 IM 账号绑定器（QA 审计 C6，main 装配时调用）。
func (h *UserHandler) SetIMAccountBinder(b IMAccountBinder) { h.imBinder = b }

// SetIMAccountResolver 注入 IM 账号查询器。
func (h *UserHandler) SetIMAccountResolver(r IMAccountResolver) { h.imResolver = r }

// Register 挂载用户管理路由。
func (h *UserHandler) Register(g *echo.Group) {
	g.GET("/users", h.listUsers)
	// T2.6/M1：管理员建用户（原来只能种子/DB 直建）。权限点 user.create 由 RouteGuard 登记。
	g.POST("/users", h.createUser)
	g.PATCH("/users/:id", h.updateUser)
	// T2.6/M1：管理员重置他人密码（权限 user.update）。重置后强制改密 + 吊销旧 token。
	g.POST("/users/:id/reset-password", h.resetPassword)
	// QA 审计 C6：IM 账号绑定 API（原 Mapper.BindAccount 全仓 0 调用方，
	// 用户无法绑定 IM → ResolveUser 永远 ErrNotBound → 所有 IM 操作 403）。
	g.POST("/users/:id/im-accounts", h.bindIMAccount)
	g.GET("/users/:id/im-accounts", h.listIMAccounts)
}

// createUserReq 创建用户请求（管理员建号，M1）。
type createUserReq struct {
	Username string  `json:"username"` // 登录名，必填，唯一
	Email    string  `json:"email"`    // 邮箱，必填，唯一
	Name     string  `json:"name"`     // 显示名，可选
	Timezone *string `json:"timezone"` // 时区，可选（缺省走 schema 默认 Asia/Shanghai）
	Password string  `json:"password"` // 初始密码，必填，须过强度校验
}

// createUser 管理员创建用户（M1，T2.6）。
//
// 权限点 user.create 由 RouteGuard 登记（POST /users）。
// 新用户置 must_change_password=true：管理员设的初始密码只应急，用户首登须改密
// （复用 forcePasswordGuard 中间件闭环，杜绝初始密码长期可用）。
// 重复 username/email 命中唯一约束，归一返 409（不泄底层 SQL）。
//
// @Summary      创建用户
// @Description  管理员建号：username/email 必填且唯一，设初始密码（须改密），可选 name/timezone。
// @Tags         user
// @Accept       json
// @Produce      json
// @Param        body  body      createUserReq  true  "用户信息"
// @Success      201  {object} ent.User
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      409  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users [post]
func (h *UserHandler) createUser(c *echo.Context) error {
	var req createUserReq
	if err := c.Bind(&req); err != nil {
		return errs.BadRequest(c, "invalid body")
	}
	if req.Username == "" || req.Email == "" {
		return errs.BadRequest(c, "username and email required")
	}
	// 初始密码同样走强度校验：管理员不能设一个弱到首登也改不动的口令（且 must_change 要求非空）。
	if msg := ValidatePasswordStrength(req.Password); msg != "" {
		return errs.BadRequest(c, msg)
	}
	b := h.db.User.Create().
		SetUsername(req.Username).
		SetEmail(req.Email).
		SetPasswordHash(HashPassword(req.Password)).
		// 首登强制改密：管理员设的初始密码仅应急（与默认 admin/seed 同策略）。
		SetMustChangePassword(true)
	if req.Name != "" {
		b.SetName(req.Name)
	}
	if req.Timezone != nil {
		b.SetTimezone(*req.Timezone)
	}
	u, err := b.Save(c.Request().Context())
	if err != nil {
		// username/email 唯一约束冲突 → 409（不泄底层 SQL），其余 → 500。
		return errs.FailConstraint(c, nil, err, "user", "username or email already exists")
	}
	// M1：建用户落审计（账号生命周期高危动作，谁在何时建了谁）。
	if h.audit != nil {
		actorID, _ := UserIDFromContext(c.Request().Context())
		e := AuditEntryFromRequest(c.Request(), actorID, "")
		e.Action = ActionUserCreate
		e.ResourceType = "user"
		e.ResourceID = u.ID
		e.ResourceName = u.Username
		h.audit.MustRecord(c.Request().Context(), e)
	}
	return c.JSON(http.StatusCreated, u)
}

// resetPasswordReq 管理员重置他人密码请求。
type resetPasswordReq struct {
	NewPassword string `json:"new_password"` // 新密码，必填，须过强度校验
}

// resetPassword 管理员重置指定用户的密码（M1，T2.6）。
//
// 权限点 user.update 由 RouteGuard 登记（POST /users/:id/reset-password）。
// 与用户自助改密（/auth/change-password）区别：管理员重置无需旧密码（用户已失联/忘记），
// 但同样置 must_change_password=true（重置后是管理员知道的临时密码，用户首登必须改），
// 并 AddTokenVersion(1) 自增令牌版本——复用 T0.4 吊销机制，使被重置用户所有已签发的
// access/refresh token 立即失效（账号疑似泄露时管理员重置即等同强制下线，是重置的核心价值）。
//
// @Summary      管理员重置密码
// @Description  管理员重置指定用户密码：无需旧密码，重置后强制改密并吊销该用户所有旧 token。
// @Tags         user
// @Accept       json
// @Produce      json
// @Param        id    path      int                 true  "用户 ID"
// @Param        body  body      resetPasswordReq    true  "新密码"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /users/{id}/reset-password [post]
func (h *UserHandler) resetPassword(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	var req resetPasswordReq
	if err := c.Bind(&req); err != nil {
		return errs.BadRequest(c, "invalid body")
	}
	if msg := ValidatePasswordStrength(req.NewPassword); msg != "" {
		return errs.BadRequest(c, msg)
	}
	// 先确认用户存在（重置不存在的用户应返 404，而非静默无操作）。
	target, err := h.db.User.Get(c.Request().Context(), id)
	if err != nil {
		return errs.FailNotFound(c, nil, err, "user")
	}
	// SetMustChangePassword(true)：重置后是临时密码，用户首登必须自己改。
	// AddTokenVersion(1)：吊销被重置用户所有旧 token（T0.4），账号疑似泄露时即强制下线。
	if err := h.db.User.UpdateOneID(id).
		SetPasswordHash(HashPassword(req.NewPassword)).
		SetMustChangePassword(true).
		AddTokenVersion(1).
		Exec(c.Request().Context()); err != nil {
		return errs.FailNotFound(c, nil, err, "user")
	}
	// M1：重置密码落审计（吊销他人 token = 强制下线，高危，须可追溯）。
	if h.audit != nil {
		actorID, _ := UserIDFromContext(c.Request().Context())
		e := AuditEntryFromRequest(c.Request(), actorID, "")
		e.Action = ActionUserResetPassword
		e.ResourceType = "user"
		e.ResourceID = target.ID
		e.ResourceName = target.Username
		h.audit.MustRecord(c.Request().Context(), e)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
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

// updateUserReq 更新用户请求（name/status/timezone/phone，不改密码）。
type updateUserReq struct {
	Name     *string `json:"name"`
	Status   *string `json:"status"` // active|disabled
	Timezone *string `json:"timezone"`
	// Phone 电话号码（B8）：SMS/语音通道按 User.phone 解号，原 schema 有字段但无 API 可写，
	// 导致电话/短信降级链虽接通却永远解不出号码。放开写入使电话兜底真正可用。
	Phone *string `json:"phone"`
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
	// 启停（改 status）是比改名/时区更敏感的动作：路由级守卫已确保 user.update，
	// 这里对 status 变更叠加 user.disable（对应 09-admin-rbac §2「启用/停用」）。
	// authz 未注入时跳过细分校验，退化为仅 user.update 门禁（不破坏测试装配）。
	if req.Status != nil && h.authz != nil {
		uid, ok := UserIDFromContext(c.Request().Context())
		if !ok {
			return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
		}
		allowed, err := h.authz.Check(c.Request().Context(), AuthzRequest{UserID: uid, Permission: PermUserDisable})
		if err != nil {
			return errs.Internal(c, nil, err)
		}
		if !allowed {
			return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "forbidden: user.disable required to change status"})
		}
	}
	// 改 status 前先取旧值：审计只在 status 真正发生变化（启用↔禁用）时记一条，
	// 避免"提交相同 status"也刷审计日志（噪音）。查不到旧值时 prevStatus 为空，
	// 后续按"有变化"处理（宁可多记一条也不漏记禁用这类高危动作）。
	var prevStatus user.Status
	if req.Status != nil {
		if old, gerr := h.db.User.Get(c.Request().Context(), id); gerr == nil {
			prevStatus = old.Status
		}
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
	if req.Phone != nil {
		u.SetPhone(*req.Phone)
	}
	updated, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "user")
	}
	// C21：用户启停落审计（禁用是高危动作，须可追溯"谁在何时停用了谁"）。
	if req.Status != nil && updated.Status != prevStatus {
		h.auditStatusChange(c, updated, prevStatus)
	}
	return c.JSON(http.StatusOK, updated)
}

// auditStatusChange 记录用户启停审计（C21）。禁用/启用分别用不同 action 便于检索。
func (h *UserHandler) auditStatusChange(c *echo.Context, u *ent.User, prev user.Status) {
	if h.audit == nil {
		return
	}
	action := ActionUserEnable
	if u.Status == user.StatusDisabled {
		action = ActionUserDisable
	}
	actorID, _ := UserIDFromContext(c.Request().Context())
	e := AuditEntryFromRequest(c.Request(), actorID, "")
	e.Action = action
	e.ResourceType = "user"
	e.ResourceID = u.ID
	e.ResourceName = u.Username
	e.Detail = map[string]any{"from": string(prev), "to": string(u.Status)}
	h.audit.MustRecord(c.Request().Context(), e)
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
	db    *ent.Client
	audit *AuditRecorder // 可选：团队成员增删留痕（T2.7，nil 时跳过）
	authz *Authorizer    // 可选：成员管理的团队软隔离校验（跨团队拒，T2.7）
}

// NewTeamHandler 创建团队 handler。
func NewTeamHandler(db *ent.Client) *TeamHandler {
	return &TeamHandler{db: db}
}

// SetAuditRecorder 注入审计记录器（T2.7：团队成员增删留痕，main 装配时调用）。
func (h *TeamHandler) SetAuditRecorder(r *AuditRecorder) { h.audit = r }

// SetAuthorizer 注入鉴权器（T2.7 团队软隔离）。
// 路由级 RouteGuard 只校验「持有 team.member.manage（org 或任意 team scope）」，
// 但目标团队来自 :id path param（非 team_id，parseTeamScope 读不到），无法做资源级隔离。
// 注入后，成员增删按目标团队 id 作为 scope 再校验一次——team 级管理员只能管自己团队的成员，
// 不能跨团队增删（团队软隔离，data-model §5 / 09-admin-rbac §3）。未注入则退化为仅路由级门禁。
func (h *TeamHandler) SetAuthorizer(a *Authorizer) { h.authz = a }

// Register 挂载团队管理路由。
func (h *TeamHandler) Register(g *echo.Group) {
	g.GET("/teams", h.listTeams)
	g.POST("/teams", h.createTeam)
	g.PATCH("/teams/:id", h.updateTeam)
	g.DELETE("/teams/:id", h.deleteTeam)
	// T2.7/M3/S15：团队成员增删。权限点 team.member.manage（悬空点落地）由 RouteGuard 登记。
	g.GET("/teams/:id/members", h.listMembers)
	g.POST("/teams/:id/members", h.addMember)
	g.DELETE("/teams/:id/members/:uid", h.removeMember)
}

// createTeamReq 创建团队请求。
type createTeamReq struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	// ParentTeamID 父团队（仅组织展示，权限不继承，见 09-admin-rbac §3）。
	// schema 有 parent_team_id 字段但原 API 不收，导致团队树无法通过 API 组织——本轮放开。
	ParentTeamID *string `json:"parent_team_id"`
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
	if req.ParentTeamID != nil {
		b.SetParentTeamID(*req.ParentTeamID)
	}
	t, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "team", "team slug or name already exists")
	}
	return c.JSON(http.StatusCreated, t)
}

// updateTeamReq 更新团队请求。
type updateTeamReq struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	ParentTeamID *string `json:"parent_team_id"` // 父团队（仅组织展示，权限不继承）
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
	if req.ParentTeamID != nil {
		u.SetParentTeamID(*req.ParentTeamID)
	}
	t, err := u.Save(c.Request().Context())
	if err != nil {
		if ent.IsNotFound(err) {
			return errs.FailNotFound(c, nil, err, "team")
		}
		return errs.FailConstraint(c, nil, err, "team", "team slug or name already exists")
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

// === Team 成员管理（M3 / S15，T2.7）===
//
// 成员是 Team↔User 直接多对多（schema Team.Edges: To("users")）。加入/移除成员是
// 「数据归属边界」的调整，权限点 team.member.manage（原悬空点，本轮落地）由 RouteGuard 登记。
// 注意：成员关系（归属）与 RBAC 角色（权限）解耦——加入团队不自动授予任何角色，
// 权限仍由 team-scope RoleBinding 单独表达（软隔离，见 09-admin-rbac §3）。

// memberReq 加成员请求。
type memberReq struct {
	UserID int `json:"user_id"`
}

// listMembers 列出团队成员（用户列表，password_hash 由 ent Sensitive 自动脱敏）。
//
// @Summary      团队成员列表
// @Tags         team
// @Produce      json
// @Param        id    path      int   true  "团队 ID"
// @Success      200  {array}   ent.User
// @Failure      404  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams/{id}/members [get]
func (h *TeamHandler) listMembers(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	users, err := h.db.User.Query().Where(user.HasTeamsWith(team.IDEQ(id))).All(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, users)
}

// addMember 把用户加入团队（幂等：已是成员再加无副作用）。
//
// 权限点 team.member.manage 由 RouteGuard 登记（POST /teams/:id/members）。
//
// @Summary      加入团队成员
// @Tags         team
// @Accept       json
// @Produce      json
// @Param        id    path      int         true  "团队 ID"
// @Param        body  body      memberReq   true  "用户 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams/{id}/members [post]
func (h *TeamHandler) addMember(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	var req memberReq
	if err := c.Bind(&req); err != nil {
		return errs.BadRequest(c, "invalid body")
	}
	if req.UserID == 0 {
		return errs.BadRequest(c, "user_id required")
	}
	// 先确认团队与用户都存在，避免 AddUserIDs 对不存在的 user 静默建悬空关系或返 500 泄底层。
	if _, err := h.db.Team.Get(c.Request().Context(), id); err != nil {
		return errs.FailNotFound(c, nil, err, "team")
	}
	// 团队软隔离：目标团队 id 作 scope 再校验一次，team 级管理员不能跨团队管成员（T2.7）。
	if err := h.checkTeamScope(c, id); err != nil {
		return err
	}
	if _, err := h.db.User.Get(c.Request().Context(), req.UserID); err != nil {
		return errs.FailNotFound(c, nil, err, "user")
	}
	if err := h.db.Team.UpdateOneID(id).AddUserIDs(req.UserID).Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	h.auditMember(c, ActionTeamMemberAdd, id, req.UserID)
	return c.NoContent(http.StatusNoContent)
}

// removeMember 把用户移出团队（幂等：非成员再移无副作用）。
//
// 权限点 team.member.manage 由 RouteGuard 登记（DELETE /teams/:id/members/:uid）。
//
// 关于该成员在本团队的 team-scope RoleBinding 处置：
//
//	本端点只解除「归属」（Team↔User 边），不联动删除其 team-scope 角色绑定。
//	理由——① 成员关系与权限授予是两条正交链路（软隔离设计基线第 6/7 条），删归属不该
//	隐式撤权，否则「临时移出再加回」会丢失精心配置的角色；② 悬空的 team-scope RoleBinding
//	不产生越权：鉴权 checkAccess 按资源实际 team 反查（scope.go），用户已非成员则访问该
//	团队资源仍被 SEC-01 团队软隔离拦截，绑定形同虚设但无害；③ 如需彻底回收权限，走
//	DELETE /role-bindings/:id 显式撤销（审计独立留痕），职责单一更可控。
//	故此处不动 RoleBinding —— 撤权是显式动作，不搭车在移除成员里。
//
// @Summary      移除团队成员
// @Tags         team
// @Param        id    path      int   true  "团队 ID"
// @Param        uid   path      int   true  "用户 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /teams/{id}/members/{uid} [delete]
func (h *TeamHandler) removeMember(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	uid, err := strconv.Atoi(c.Param("uid"))
	if err != nil {
		return errs.BadRequest(c, "invalid user id")
	}
	if _, err := h.db.Team.Get(c.Request().Context(), id); err != nil {
		return errs.FailNotFound(c, nil, err, "team")
	}
	// 团队软隔离：目标团队 id 作 scope 再校验一次（跨团队拒，T2.7）。
	if err := h.checkTeamScope(c, id); err != nil {
		return err
	}
	if err := h.db.Team.UpdateOneID(id).RemoveUserIDs(uid).Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	h.auditMember(c, ActionTeamMemberRemove, id, uid)
	return c.NoContent(http.StatusNoContent)
}

// errTeamAccessDenied 哨兵：checkTeamScope 已写响应（403/401/500）后返回它，
// 调用方据此 return 中止后续写操作。不能返回 errs.Forbidden 的返回值——echo v5 的
// c.JSON 写完返回 nil，调用方会误以为通过而继续执行（越权），故须用非 nil 哨兵显式中止。
var errTeamAccessDenied = errors.New("team access denied (response already written)")

// checkTeamScope 以目标团队 id 作 scope 校验操作者持有 team.member.manage（团队软隔离，T2.7）。
// authz 未注入（测试/降级）返回 nil 放行。org 级持有者对所有团队通过；team 级仅本团队通过。
// 校验不通过时写响应并返回 errTeamAccessDenied 哨兵，调用方须 return 中止。
func (h *TeamHandler) checkTeamScope(c *echo.Context, teamID int) error {
	if h.authz == nil {
		return nil
	}
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		_ = errs.Unauthorized(c, "not authenticated")
		return errTeamAccessDenied
	}
	allowed, err := h.authz.Check(c.Request().Context(), AuthzRequest{
		UserID:     uid,
		Permission: PermTeamMemberManage,
		TeamScope:  &teamID,
	})
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errTeamAccessDenied
	}
	if !allowed {
		_ = errs.Forbidden(c, "forbidden: team.member.manage required for this team")
		return errTeamAccessDenied
	}
	return nil
}

// auditMember 记录团队成员增删审计（T2.7）。best-effort，audit 未注入时跳过。
func (h *TeamHandler) auditMember(c *echo.Context, action string, teamID, userID int) {
	if h.audit == nil {
		return
	}
	actorID, _ := UserIDFromContext(c.Request().Context())
	e := AuditEntryFromRequest(c.Request(), actorID, "")
	e.Action = action
	e.ResourceType = "team"
	e.ResourceID = teamID
	e.Detail = map[string]any{"user_id": userID}
	h.audit.MustRecord(c.Request().Context(), e)
}
