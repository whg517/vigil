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

	"github.com/labstack/echo/v4"
)

// AuthHandler 登录态 API handler（与 RBAC 的 Handler 区分）。
type AuthHandler struct {
	db     *ent.Client
	signer *JWTSigner
}

// NewAuthHandler 创建登录态 handler。signer 为 nil 时签发链路返回 500（降级保护）。
func NewAuthHandler(db *ent.Client, signer *JWTSigner) *AuthHandler {
	return &AuthHandler{db: db, signer: signer}
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

// RegisterProtected 挂载需已登录态的路由（me）。挂到 v1（带 RequireUser）。
func (h *AuthHandler) RegisterProtected(g *echo.Group) {
	g.GET("/auth/me", h.me)
}

// toLoginUser 把 ent.User 裁剪为对前端安全的视图（不含 password_hash）。
func toLoginUser(u *ent.User) loginUser {
	return loginUser{
		ID: u.ID, Username: u.Username, Name: u.Name,
		Email: u.Email, Status: string(u.Status),
	}
}

func (h *AuthHandler) login(c echo.Context) error {
	var req loginReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "username and password required"})
	}
	if h.signer == nil || !h.signer.Available() {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "jwt not configured"})
	}
	u, err := h.db.User.Query().Where(user.UsernameEQ(req.Username)).Only(c.Request().Context())
	if err != nil {
		// 用户不存在也返回 invalid credentials（避免用户名枚举）
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}
	if !VerifyPassword(req.Password, u.PasswordHash) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}
	if u.Status != user.StatusActive {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "user disabled"})
	}
	access, err := h.signer.GenerateAccessToken(u.ID, u.Username)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	refresh, err := h.signer.GenerateRefreshToken(u.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, loginResp{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		User: toLoginUser(u),
	})
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *AuthHandler) refresh(c echo.Context) error {
	var req refreshReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "refresh_token required"})
	}
	if h.signer == nil || !h.signer.Available() {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "jwt not configured"})
	}
	claims, err := h.signer.ParseToken(req.RefreshToken)
	if err != nil || claims.TokenType != TokenTypeRefresh {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid refresh token"})
	}
	access, err := h.signer.GenerateAccessToken(claims.UserID, claims.Username)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"access_token": access, "token_type": "Bearer"})
}

func (h *AuthHandler) me(c echo.Context) error {
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
	}
	u, err := h.db.User.Get(c.Request().Context(), uid)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "user not found"})
	}
	return c.JSON(http.StatusOK, toLoginUser(u))
}
