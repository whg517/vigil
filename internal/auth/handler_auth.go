// handler_auth.go 登录态 API（能力域 13 §登录）：login / refresh / me。
//
// 登录链路：username+password → bcrypt 校验 → 签发 access+refresh。
// 刷新链路：refresh token → 校验 → 签发新 access（refresh 不轮换，简化）。
// me：从鉴权 context 取 userID 返回当前用户信息。
//
// 与 RBAC 的 Handler 区分：本 handler 只管登录态（换 token / 看自己），
// 角色绑定管理仍在 handler.go。login/refresh 走 public group（无需已登录），
// me 走 v1（RequireUser 保护）。
package auth

import (
	"net/http"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// AuthHandler 登录态 API handler（与 RBAC 的 Handler 区分）。
type AuthHandler struct {
	db         *ent.Client
	signer     *JWTSigner
	audit      *AuditRecorder // 审计记录器（可选，登录成功/失败都记）
	loginGuard *LoginGuard    // 登录限流/锁定（可选，SEC-04）
}

// NewAuthHandler 创建登录态 handler。signer 为 nil 时签发链路返回 500（降级保护）。
func NewAuthHandler(db *ent.Client, signer *JWTSigner) *AuthHandler {
	return &AuthHandler{db: db, signer: signer}
}

// SetAuditRecorder 注入审计记录器（main 装配时调用）。
func (h *AuthHandler) SetAuditRecorder(r *AuditRecorder) {
	h.audit = r
}

// SetLoginGuard 注入登录防护器（SEC-04，main 装配时调用）。
// 为 nil 时降级为不限流（依赖审计日志事后追溯）。
func (h *AuthHandler) SetLoginGuard(g *LoginGuard) {
	h.loginGuard = g
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginUser 返回给前端的用户信息（裁剪敏感字段，避免泄露 password_hash）。
type loginUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Status   string `json:"status"`
}

type loginResp struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	User         loginUser `json:"user"`
}

// RegisterPublic 挂载无需已登录态的路由（login/refresh）。挂到 public group。
func (h *AuthHandler) RegisterPublic(g *echo.Group) {
	g.POST("/auth/login", h.login)
	g.POST("/auth/refresh", h.refresh)
}

// RegisterProtected 挂载需已登录态的路由（me / change-password）。挂到 v1（带 RequireUser）。
func (h *AuthHandler) RegisterProtected(g *echo.Group) {
	g.GET("/auth/me", h.me)
	g.POST("/auth/change-password", h.changePassword)
}

type changePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// changePassword 修改当前用户密码（QA 审计 C8 强制改密闭环）。
// 校验旧密码 + 新密码强度，成功后清除 must_change_password 标志。
// 默认 admin 被中间件标记 must_change_password=true 后，唯一出路就是此端点改密。
//
// @Summary      修改密码
// @Description  校验旧密码并设置新密码，成功清除强制改密标志。
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body  changePasswordReq  true  "旧密码 + 新密码"
// @Success      200   {object}  map[string]string
// @Failure      400   {object}  httputil.ErrorResponse
// @Failure      401   {object}  httputil.ErrorResponse
// @Failure      500   {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /auth/change-password [post]
func (h *AuthHandler) changePassword(c *echo.Context) error {
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
	}
	var req changePasswordReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "old_password and new_password required"})
	}
	if msg := ValidatePasswordStrength(req.NewPassword); msg != "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: msg})
	}
	if req.OldPassword == req.NewPassword {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "new password must differ from old"})
	}
	u, err := h.db.User.Get(c.Request().Context(), uid)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "user not found"})
	}
	if !VerifyPassword(req.OldPassword, u.PasswordHash) {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "invalid old password"})
	}
	if err := h.db.User.UpdateOneID(uid).
		SetPasswordHash(HashPassword(req.NewPassword)).
		SetMustChangePassword(false).
		Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// toLoginUser 把 ent.User 裁剪为对前端安全的视图（不含 password_hash）。
func toLoginUser(u *ent.User) loginUser {
	return loginUser{
		ID: u.ID, Username: u.Username, Name: u.Name,
		Email: u.Email, Status: string(u.Status),
	}
}

// login 用户名密码登录，换取 access+refresh token。
//
// @Summary      登录
// @Description  username+password 校验通过后签发 access+refresh token。
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body     loginReq   true  "登录凭证"
// @Success      200   {object} auth.loginResp
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      401   {object} httputil.ErrorResponse
// @Failure      403   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Router       /auth/login [post]
func (h *AuthHandler) login(c *echo.Context) error {
	var req loginReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "username and password required"})
	}
	if h.signer == nil || !h.signer.Available() {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: "jwt not configured"})
	}
	// SEC-04：登录前双维度限流 + 锁定检查（无 Redis 时降级跳过）。
	ip := ClientIP(c.Request().Header.Get("X-Forwarded-For"), c.Request().RemoteAddr)
	if h.loginGuard != nil {
		if allowed, reason := h.loginGuard.Check(c.Request().Context(), ip, req.Username); !allowed {
			h.auditLogin(c, 0, req.Username, AuditResultDenied, map[string]any{"reason": "rate_limited", "ip": ip})
			return c.JSON(http.StatusTooManyRequests, httputil.ErrorResponse{Error: reason})
		}
	}
	u, err := h.db.User.Query().Where(user.UsernameEQ(req.Username)).Only(c.Request().Context())
	if err != nil {
		// 用户不存在也返回 invalid credentials（避免用户名枚举）
		h.recordLoginFailure(c, 0, req.Username, "user_not_found", ip)
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "invalid credentials"})
	}
	if !VerifyPassword(req.Password, u.PasswordHash) {
		h.recordLoginFailure(c, u.ID, u.Username, "wrong_password", ip)
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "invalid credentials"})
	}
	if u.Status != user.StatusActive {
		h.auditLogin(c, u.ID, u.Username, AuditResultDenied, map[string]any{"reason": "user_disabled"})
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "user disabled"})
	}
	access, err := h.signer.GenerateAccessToken(u.ID, u.Username)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	refresh, err := h.signer.GenerateRefreshToken(u.ID)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	// 登录成功：清零该账号失败计数。
	if h.loginGuard != nil {
		h.loginGuard.RecordSuccess(c.Request().Context(), u.Username)
	}
	h.auditLogin(c, u.ID, u.Username, AuditResultSuccess, nil)
	return c.JSON(http.StatusOK, loginResp{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		User: toLoginUser(u),
	})
}

// recordLoginFailure 统一处理登录失败：记审计 + 累加失败计数（SEC-04）。
func (h *AuthHandler) recordLoginFailure(c *echo.Context, uid int, username, reason, ip string) {
	h.auditLogin(c, uid, username, AuditResultFailed, map[string]any{"reason": reason, "ip": ip})
	if h.loginGuard != nil {
		h.loginGuard.RecordFailure(c.Request().Context(), username)
	}
}

// auditLogin 记录登录审计（actor_user_id 可能 0=用户不存在，actor_name 记 username 用于溯源）。
func (h *AuthHandler) auditLogin(c *echo.Context, uid int, username string, result AuditResult, detail map[string]any) {
	if h.audit == nil {
		return
	}
	e := AuditEntryFromRequest(c.Request(), uid, username)
	e.Action = "auth.login"
	e.ResourceType = "user"
	e.ResourceID = uid
	e.ResourceName = username
	e.Result = result
	if detail != nil {
		e.Detail = detail
	}
	h.audit.MustRecord(c.Request().Context(), e)
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

// refresh 用 refresh token 换取新的 access token。
//
// @Summary      刷新 token
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body     refreshReq  true  "refresh token"
// @Success      200   {object} map[string]string
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      401   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Router       /auth/refresh [post]
func (h *AuthHandler) refresh(c *echo.Context) error {
	var req refreshReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "refresh_token required"})
	}
	if h.signer == nil || !h.signer.Available() {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: "jwt not configured"})
	}
	claims, err := h.signer.ParseToken(req.RefreshToken)
	if err != nil || claims.TokenType != TokenTypeRefresh {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "invalid refresh token"})
	}
	access, err := h.signer.GenerateAccessToken(claims.UserID, claims.Username)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"access_token": access, "token_type": "Bearer"})
}

// me 当前登录用户信息。
//
// @Summary      当前用户信息
// @Tags         auth
// @Produce      json
// @Success      200  {object} auth.loginUser
// @Failure      401  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /auth/me [get]
func (h *AuthHandler) me(c *echo.Context) error {
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
	}
	u, err := h.db.User.Get(c.Request().Context(), uid)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "user not found"})
	}
	return c.JSON(http.StatusOK, toLoginUser(u))
}
