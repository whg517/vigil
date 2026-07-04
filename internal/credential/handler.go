// Package credential 实现 Runbook 执行器凭据的加密托管管理面（T6.3 / 审计 S16）。
//
// 对应 docs/capabilities/06-runbook.md §7 Q1「执行器的凭证管理 → 加密存储于 Vigil，admin 管理」。
//
// 管理面：CRUD 凭据（create/update 收明文 secret → AES-256-GCM 加密落库；
// list/get 只返元数据，密文经 Sensitive 恒不回显，明文永不回显——类似 API Key 语义）。
// 归属沿用 Team 软隔离（team_id=0 为 org 级）。权限点 credential.{view,create,update,delete}
// 授给 team_admin/org_admin。
//
// ★ 安全红线：明文 secret 只在 create/update 请求体入站 → 加密这一瞬间存在于内存，
// 加密后即丢弃；绝不回显、不落日志/审计（审计只记名/类型/id）。
package credential

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entcredential "github.com/kevin/vigil/ent/credential"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 立即 return 中止后续逻辑。
// 背景同 ticket/runbook handler（errs.Forbidden 写完响应返回 nil，须换非 nil 哨兵防越权落库）。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler 凭据托管管理 API。
type Handler struct {
	db     *ent.Client
	cipher *crypto.Cipher      // 加密器（nil 则凭据托管未启用，create/update 拒绝）
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
	audit  *auth.AuditRecorder // 配置变更留痕（可选注入）
}

// NewHandler 创建凭据 handler。cipher 为 nil 时功能降级（create/update 返回 503，list/get/delete 仍可用）。
func NewHandler(db *ent.Client, cipher *crypto.Cipher) *Handler {
	return &Handler{db: db, cipher: cipher}
}

// SetAuthorizer 注入鉴权器。为 nil 时降级为无资源级校验（兼容渐进/单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（凭据配置变更留痕）。
func (h *Handler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/credentials", h.list)
	g.POST("/credentials", h.create)
	g.GET("/credentials/:id", h.get)
	g.PATCH("/credentials/:id", h.update)
	g.DELETE("/credentials/:id", h.delete)
}

func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权（SEC-01）。authz/scope 为 nil 时放行（渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "credential", id)
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		_ = errs.Forbidden(c, "")
		return errAccessDenied
	}
	return nil
}

// auditChange 记录凭据配置变更审计（只记元数据：名/类型/id，绝不含明文/密文）。
func (h *Handler) auditChange(c *echo.Context, action string, cred *ent.Credential) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
	e.Action = action
	e.ResourceType = "credential"
	e.ResourceID = cred.ID
	e.ResourceName = cred.Name
	e.Detail = map[string]any{"type": cred.Type.String()} // 类型是元数据，明文/密文不入审计
	h.audit.MustRecord(c.Request().Context(), e)
}

// createReq 创建凭据请求。secret 为明文，仅入不出（加密落库后不回显）。
type createReq struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`   // bearer|token|basic|header（默认 bearer）
	Secret string         `json:"secret"` // 明文凭据，加密后落库，永不回显
	Config map[string]any `json:"config"` // header 类型的头名等
	TeamID int            `json:"team_id"`
}

// updateReq 更新凭据请求（全指针，部分更新）。传 secret 则重加密替换。
type updateReq struct {
	Name   *string         `json:"name"`
	Type   *string         `json:"type"`
	Secret *string         `json:"secret"` // 传则更新明文（重加密），不回显
	Config *map[string]any `json:"config"`
}

// list 凭据列表（密文不回显，Sensitive 字段序列化时忽略）。
//
// @Summary      凭据列表
// @Tags         credential
// @Produce      json
// @Success      200  {array}   ent.Credential
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /credentials [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Credential.Query()
	// SEC-01 list 数据隔离：team 级用户仅见本团队归属的凭据；org 级全可见。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.Credential{})
				}
				q = q.Where(entcredential.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	creds, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, creds)
}

// create 创建凭据（明文 secret 加密后落库）。
//
// @Summary      创建凭据
// @Tags         credential
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "凭据（含明文 secret，加密存储）"
// @Success      201  {object} ent.Credential
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      503  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /credentials [post]
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Secret == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and secret required"})
	}
	// 未配加密密钥：拒绝创建（绝不明文兜底落库）。
	if h.cipher == nil {
		return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{
			Error: "credential encryption not enabled: set VIGIL_CREDENTIAL_ENCRYPTION_KEY",
		})
	}
	typ := req.Type
	if typ == "" {
		typ = "bearer"
	}
	if e := h.checkCreateAccess(c, req.TeamID); e != nil {
		return e
	}
	ciphertext, err := h.cipher.Encrypt(req.Secret)
	if err != nil {
		return errs.Internal(c, nil, err) // 加密失败（不含明文）
	}
	req.Secret = "" // 明文用完即弃
	b := h.db.Credential.Create().
		SetName(req.Name).
		SetType(entcredential.Type(typ)).
		SetSecretCiphertext(ciphertext)
	if req.Config != nil {
		b.SetConfig(req.Config)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	cred, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "credential", "credential already exists")
	}
	h.auditChange(c, auth.ActionCredentialCreate, cred)
	return c.JSON(http.StatusCreated, cred)
}

// checkCreateAccess 创建鉴权：非 org 级用户只能给可见 team 建，跨团队/建无主凭据拒（403 不落库）。
func (h *Handler) checkCreateAccess(c *echo.Context, teamID int) error {
	if h.authz == nil {
		return nil // 渐进/单测降级
	}
	uid := h.actorFromContext(c)
	if uid <= 0 {
		return nil
	}
	visible, orgWide, err := h.authz.VisibleTeamIDs(c.Request().Context(), uid)
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if orgWide {
		return nil // org 级：可建任意归属（含无主）
	}
	if teamID <= 0 {
		_ = errs.Forbidden(c, "team-scoped user must create credential under a visible team")
		return errAccessDenied
	}
	for _, id := range visible {
		if id == teamID {
			return nil
		}
	}
	_ = errs.Forbidden(c, "no access to target team")
	return errAccessDenied
}

// get 凭据详情（密文不回显）。
//
// @Summary      凭据详情
// @Tags         credential
// @Produce      json
// @Param        id   path      int  true  "凭据 ID"
// @Success      200  {object} ent.Credential
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /credentials/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermCredentialView); e != nil {
		return e
	}
	cred, err := h.db.Credential.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "credential not found"})
	}
	return c.JSON(http.StatusOK, cred)
}

// update 更新凭据（传 secret 则重加密替换）。
//
// @Summary      更新凭据
// @Tags         credential
// @Accept       json
// @Produce      json
// @Param        id    path      int        true  "凭据 ID"
// @Param        body  body      updateReq  true  "更新字段"
// @Success      200  {object} ent.Credential
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /credentials/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermCredentialUpdate); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.Credential.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Type != nil {
		u.SetType(entcredential.Type(*req.Type))
	}
	if req.Secret != nil {
		// 换密钥前必须有加密能力（无密钥不允许写入新密文）。
		if h.cipher == nil {
			return c.JSON(http.StatusServiceUnavailable, httputil.ErrorResponse{
				Error: "credential encryption not enabled: set VIGIL_CREDENTIAL_ENCRYPTION_KEY",
			})
		}
		if *req.Secret == "" {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "secret cannot be empty"})
		}
		ciphertext, err := h.cipher.Encrypt(*req.Secret)
		if err != nil {
			return errs.Internal(c, nil, err)
		}
		*req.Secret = "" // 明文用完即弃
		u.SetSecretCiphertext(ciphertext)
	}
	if req.Config != nil {
		u.SetConfig(*req.Config)
	}
	cred, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "credential")
	}
	h.auditChange(c, auth.ActionCredentialUpdate, cred)
	return c.JSON(http.StatusOK, cred)
}

// delete 删除凭据。
//
// @Summary      删除凭据
// @Tags         credential
// @Param        id   path      int  true  "凭据 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /credentials/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermCredentialDelete); e != nil {
		return e
	}
	victim, _ := h.db.Credential.Get(c.Request().Context(), id)
	if err := h.db.Credential.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	if victim == nil {
		victim = &ent.Credential{ID: id}
	}
	h.auditChange(c, auth.ActionCredentialDelete, victim)
	return c.NoContent(http.StatusNoContent)
}
