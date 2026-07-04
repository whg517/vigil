// handler.go 出向工单集成配置 API（T4.3）。
//
// 工单集成是出向集成配置（type/endpoint/credential/config/归属）。管理面独立于入向接入点：
// list/get/create/update/delete。凭据经 Sensitive 字段存储，list/get 不回显（与 Integration 同款）。
package ticket

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/ticketintegration"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 立即 return 中止后续逻辑。
// 背景同 integration/postmortem handler（errs.Forbidden 写完响应返回 nil，须换非 nil 哨兵防越权落库）。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler 工单集成配置 API。
type Handler struct {
	db     *ent.Client
	cipher *crypto.Cipher      // 凭据加密器（T6.3，可选）：非 nil 时凭据加密后落库
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
	audit  *auth.AuditRecorder // 配置变更留痕（C21，可选注入）
}

// NewHandler 创建工单集成 handler。
func NewHandler(db *ent.Client) *Handler { return &Handler{db: db} }

// SetCipher 注入凭据加密器（T6.3）：非 nil 时 create/update 收到的明文凭据加密后落库
// （与 Runbook 执行器凭据复用同一 AES-256-GCM 机制）；nil 时按原样存储（向后兼容）。
func (h *Handler) SetCipher(c *crypto.Cipher) { h.cipher = c }

// encryptCredential 加密明文凭据（cipher 非 nil 时）；nil 时原样返回（向后兼容）。
// 加密失败返回 error（调用方按 500 处理），错误不含明文。
func (h *Handler) encryptCredential(plaintext string) (string, error) {
	if h.cipher == nil || plaintext == "" {
		return plaintext, nil
	}
	return h.cipher.Encrypt(plaintext)
}

// SetAuthorizer 注入鉴权器。为 nil 时降级为无资源级校验（兼容渐进/单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（T4.3：工单集成配置变更留痕）。
func (h *Handler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/ticket-integrations", h.list)
	g.POST("/ticket-integrations", h.create)
	g.GET("/ticket-integrations/:id", h.get)
	g.PATCH("/ticket-integrations/:id", h.update)
	g.DELETE("/ticket-integrations/:id", h.delete)
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
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "ticket_integration", id)
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

// auditConfigChange 记录工单集成配置变更审计（T4.3）。
func (h *Handler) auditConfigChange(c *echo.Context, action string, ti *ent.TicketIntegration) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
	e.Action = action
	e.ResourceType = "ticket_integration"
	e.ResourceID = ti.ID
	e.ResourceName = ti.Name
	h.audit.MustRecord(c.Request().Context(), e)
}

// createReq 创建工单集成请求。
type createReq struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`       // webhook|jira|zentao（默认 webhook）
	Endpoint   string         `json:"endpoint"`   // 建单目标 URL
	Credential string         `json:"credential"` // 凭据（token/密码），仅入不出（Sensitive）
	Config     map[string]any `json:"config"`     // 目标项目/字段映射
	TeamID     int            `json:"team_id"`    // 归属团队，0=org 级
}

// updateReq 更新工单集成请求（全指针，部分更新）。
type updateReq struct {
	Name       *string         `json:"name"`
	Endpoint   *string         `json:"endpoint"`
	Credential *string         `json:"credential"` // 传则更新凭据（不回显）
	Config     *map[string]any `json:"config"`
	Enabled    *bool           `json:"enabled"`
}

// list 工单集成列表（凭据不回显，Sensitive 字段序列化时忽略）。
//
// @Summary      工单集成列表
// @Tags         ticket-integration
// @Produce      json
// @Success      200  {array}   ent.TicketIntegration
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /ticket-integrations [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.TicketIntegration.Query()
	// SEC-01 list 数据隔离：team 级用户仅见本团队（及无 team 归属的 org 级？此处仅按 team join，
	// 与 integration handler 同款——只回本团队归属的；org 级用户全可见）。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.TicketIntegration{})
				}
				q = q.Where(ticketintegration.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	tis, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, tis)
}

// create 创建工单集成。
//
// @Summary      创建工单集成
// @Tags         ticket-integration
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "工单集成配置"
// @Success      201  {object} ent.TicketIntegration
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /ticket-integrations [post]
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and endpoint required"})
	}
	typ := req.Type
	if typ == "" {
		typ = "webhook"
	}
	// 创建鉴权：team 级用户仅能给可见 team 建（0=org 级，仅 org 级用户可建无主集成）。
	if e := h.checkCreateAccess(c, req.TeamID); e != nil {
		return e
	}
	b := h.db.TicketIntegration.Create().
		SetName(req.Name).
		SetType(ticketintegration.Type(typ)).
		SetEndpoint(req.Endpoint).
		SetEnabled(true)
	if req.Credential != "" {
		// T6.3：明文凭据加密后落库（cipher 未配时按原样存，向后兼容）。
		enc, err := h.encryptCredential(req.Credential)
		if err != nil {
			return errs.Internal(c, nil, err)
		}
		b.SetCredential(enc)
	}
	if req.Config != nil {
		b.SetConfig(req.Config)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	ti, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "ticket_integration", "ticket integration already exists")
	}
	h.auditConfigChange(c, auth.ActionTicketIntegrationCreate, ti)
	return c.JSON(http.StatusCreated, ti)
}

// checkCreateAccess 创建鉴权：非 org 级用户只能给可见 team 建，跨团队/建无主集成拒（403 不落库）。
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
	// team 级：必须指定一个可见 team（不能建 org 级无主集成）。
	if teamID <= 0 {
		_ = errs.Forbidden(c, "team-scoped user must create ticket integration under a visible team")
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

// get 工单集成详情（凭据不回显）。
//
// @Summary      工单集成详情
// @Tags         ticket-integration
// @Produce      json
// @Param        id   path      int  true  "工单集成 ID"
// @Success      200  {object} ent.TicketIntegration
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /ticket-integrations/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermTicketIntegrationView); e != nil {
		return e
	}
	ti, err := h.db.TicketIntegration.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "ticket integration not found"})
	}
	return c.JSON(http.StatusOK, ti)
}

// update 更新工单集成。
//
// @Summary      更新工单集成
// @Tags         ticket-integration
// @Accept       json
// @Produce      json
// @Param        id    path      int        true  "工单集成 ID"
// @Param        body  body      updateReq  true  "更新字段"
// @Success      200  {object} ent.TicketIntegration
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /ticket-integrations/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermTicketIntegrationUpdate); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.TicketIntegration.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Endpoint != nil {
		u.SetEndpoint(*req.Endpoint)
	}
	if req.Credential != nil {
		// T6.3：更新凭据同样加密后落库。
		enc, err := h.encryptCredential(*req.Credential)
		if err != nil {
			return errs.Internal(c, nil, err)
		}
		u.SetCredential(enc)
	}
	if req.Config != nil {
		u.SetConfig(*req.Config)
	}
	if req.Enabled != nil {
		u.SetEnabled(*req.Enabled)
	}
	ti, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "ticket_integration")
	}
	h.auditConfigChange(c, auth.ActionTicketIntegrationUpdate, ti)
	return c.JSON(http.StatusOK, ti)
}

// delete 删除工单集成。
//
// @Summary      删除工单集成
// @Tags         ticket-integration
// @Param        id   path      int  true  "工单集成 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /ticket-integrations/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermTicketIntegrationDelete); e != nil {
		return e
	}
	victim, _ := h.db.TicketIntegration.Get(c.Request().Context(), id)
	if err := h.db.TicketIntegration.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	if victim == nil {
		victim = &ent.TicketIntegration{ID: id}
	}
	h.auditConfigChange(c, auth.ActionTicketIntegrationDelete, victim)
	return c.NoContent(http.StatusNoContent)
}
