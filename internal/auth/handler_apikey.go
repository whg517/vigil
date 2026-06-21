// handler_apikey.go API Key 管理 API（能力域 13 §API Key 管理，PRD M13.7）。
//
// CRUD：list / create / delete。权限点 admin.apikey.manage（已存在）。
//
// 创建流程：
//   - 生成明文 token（vgl_<32hex>）+ SHA256 哈希
//   - 库内存哈希 + prefix（明文前 12 字符），明文仅在响应里返回一次
//   - 前端展示后由用户自行保存，丢失只能重建
//
// 鉴权模型：API Key 仅做身份标识（关联 user_id），鉴权继承归属 User 的角色。
// scope 字段预留（本期不强制收敛到 key 级权限）。
package auth

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/apikey"
	"github.com/kevin/vigil/ent/user"

	"github.com/labstack/echo/v4"
)

// APIKeyHandler API Key 管理 handler。
type APIKeyHandler struct {
	db *ent.Client
}

// NewAPIKeyHandler 创建 handler。
func NewAPIKeyHandler(db *ent.Client) *APIKeyHandler {
	return &APIKeyHandler{db: db}
}

// Register 挂载路由到业务路由组（v1，已过 RequireUser 身份解析）。
// 按权限点保护由 main 装配时挂 RequirePermPerRoute（当前 v1 全组 RequireUser 兜底）。
func (h *APIKeyHandler) Register(g *echo.Group) {
	g.GET("/api-keys", h.list)
	g.POST("/api-keys", h.create)
	g.DELETE("/api-keys/:id", h.delete)
}

// apiKeyView 对前端展示的视图（不含 token_hash，只含 prefix 便于识别）。
type apiKeyView struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      []string   `json:"scope"`
	Status     string     `json:"status"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// apiKeyCreateReq 创建请求。
type apiKeyCreateReq struct {
	Name      string   `json:"name"`
	Scope     []string `json:"scope"`
	ExpiresIn int      `json:"expires_in_hours"` // 有效期（小时），0=永久
}

// apiKeyCreateResp 创建响应（含一次性明文 token）。
type apiKeyCreateResp struct {
	apiKeyView
	Plaintext string `json:"token"` // ★ 明文 token，仅此一次返回
}

func (h *APIKeyHandler) list(c echo.Context) error {
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
	}
	// 列出当前用户的 API Key（按创建时间倒序）
	keys, err := h.db.APIKey.Query().
		Where(apikey.HasUserWith(user.IDEQ(uid))).
		Order(ent.Desc(apikey.FieldCreatedAt)).
		All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	out := make([]apiKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyView(k))
	}
	return c.JSON(http.StatusOK, out)
}

func (h *APIKeyHandler) create(c echo.Context) error {
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
	}
	var req apiKeyCreateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
	}

	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	builder := h.db.APIKey.Create().
		SetName(req.Name).
		SetTokenHash(hash).
		SetPrefix(TokenPrefix(plaintext)).
		SetUserID(uid)
	if len(req.Scope) > 0 {
		builder.SetScope(req.Scope)
	}
	if req.ExpiresIn > 0 {
		builder.SetExpiresAt(time.Now().Add(time.Duration(req.ExpiresIn) * time.Hour))
	}
	k, err := builder.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, apiKeyCreateResp{
		apiKeyView: toAPIKeyView(k),
		Plaintext:  plaintext,
	})
}

func (h *APIKeyHandler) delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	uid, ok := UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
	}
	// 只能删除自己的 key（防越权删他人 key）
	k, err := h.db.APIKey.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "api key not found"})
	}
	kUID, err := k.QueryUser().OnlyID(c.Request().Context())
	if err != nil || kUID != uid {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "cannot delete others' api key"})
	}
	if err := h.db.APIKey.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// toAPIKeyView 实体转视图（不含敏感字段）。
func toAPIKeyView(k *ent.APIKey) apiKeyView {
	return apiKeyView{
		ID: k.ID, Name: k.Name, Prefix: k.Prefix,
		Scope: k.Scope, Status: string(k.Status),
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt,
	}
}
