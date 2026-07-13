// subscription_handler.go 出站 webhook 动态订阅管理面 CRUD（N2.2）。
//
// 对应出向 webhook（ADR-0030 四方向集成）：
// 替代/补充环境变量 VIGIL_WEBHOOK_OUT_URLS（全局静态、需重启改），提供运行时按需增删的
// 动态订阅——按团队隔离、按事件类型过滤、每订阅独立签名密钥。dispatcher 出站时把 env 静态订阅
// 与 DB 动态订阅合并投递（EntSubscriptionResolver，向后兼容 env）。
//
//	GET    /webhook-subscriptions          列表（team 数据隔离）
//	POST   /webhook-subscriptions          创建
//	GET    /webhook-subscriptions/:id       详情（signing_secret 不回显）
//	PATCH  /webhook-subscriptions/:id       更新
//	DELETE /webhook-subscriptions/:id       删除
//
// 归属沿用 Team 软隔离（team_id=0 为 org 级）。权限点 webhook_subscription.{view,create,update,delete}
// 授给 team_admin/org_admin（同 ticket_integration/credential 档）。
//
// ★ 安全：signing_secret 明文仅在 create/update 入站 → 加密（crypto.Cipher）落库这一瞬间存在于内存，
// 之后经 ent Sensitive 恒不回显、不落审计（审计只记名/id/url）。
package webhook

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/webhooksubscription"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 立即 return 中止后续逻辑。
// 背景同 credential/ticket handler（errs.Forbidden 写完响应返回 nil，须换非 nil 哨兵防越权落库）。
var errAccessDenied = errors.New("access denied (response already written)")

// SubscriptionHandler 出站 webhook 动态订阅管理 API（N2.2）。
type SubscriptionHandler struct {
	db     *ent.Client
	cipher *crypto.Cipher      // 加密 signing_secret（nil 则按明文存储，降级/单测）
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
	audit  *auth.AuditRecorder // 配置变更留痕（可选注入）
}

// NewSubscriptionHandler 创建动态订阅 handler。cipher 为 nil 时 signing_secret 明文存储（降级）。
func NewSubscriptionHandler(db *ent.Client) *SubscriptionHandler {
	return &SubscriptionHandler{db: db}
}

// SetCipher 注入凭据加密器（signing_secret 加密存储，与 credential/ticket 同源密钥）。
func (h *SubscriptionHandler) SetCipher(c *crypto.Cipher) { h.cipher = c }

// SetAuthorizer 注入鉴权器。为 nil 时降级为无资源级校验（兼容渐进/单测）。
func (h *SubscriptionHandler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer）。
func (h *SubscriptionHandler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（订阅配置变更留痕）。
func (h *SubscriptionHandler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载路由。
func (h *SubscriptionHandler) Register(g *echo.Group) {
	g.GET("/webhook-subscriptions", h.list)
	g.POST("/webhook-subscriptions", h.create)
	g.GET("/webhook-subscriptions/:id", h.get)
	g.PATCH("/webhook-subscriptions/:id", h.update)
	g.DELETE("/webhook-subscriptions/:id", h.delete)
}

func (h *SubscriptionHandler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权（SEC-01）。authz/scope 为 nil 时放行（渐进/单测）。
func (h *SubscriptionHandler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "webhook_subscription", id)
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

// checkCreateAccess 创建鉴权：非 org 级用户只能给可见 team 建，跨团队/建无主订阅拒（403 不落库）。
func (h *SubscriptionHandler) checkCreateAccess(c *echo.Context, teamID int) error {
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
		_ = errs.Forbidden(c, "team-scoped user must create subscription under a visible team")
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

// auditChange 记录订阅配置变更审计（只记元数据：名/id/url，绝不含 signing_secret）。
func (h *SubscriptionHandler) auditChange(c *echo.Context, action string, sub *ent.WebhookSubscription) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
	e.Action = action
	e.ResourceType = "webhook_subscription"
	e.ResourceID = sub.ID
	e.ResourceName = sub.Name
	e.Detail = map[string]any{"url": sub.URL} // url 是元数据，密钥不入审计
	h.audit.MustRecord(c.Request().Context(), e)
}

// createReq 创建订阅请求。signing_secret 为明文，仅入不出（加密落库后不回显）。
type createReq struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	EventTypes    []string `json:"event_types"`    // 空=所有事件类型
	SigningSecret string   `json:"signing_secret"` // 明文签名密钥，加密后落库，永不回显
	Enabled       *bool    `json:"enabled"`        // 缺省视为 true
	TeamID        int      `json:"team_id"`        // 0=org 级
}

// updateReq 更新订阅请求（全指针，部分更新）。
type updateReq struct {
	Name          *string   `json:"name"`
	URL           *string   `json:"url"`
	EventTypes    *[]string `json:"event_types"`
	SigningSecret *string   `json:"signing_secret"` // 传则更新（重加密）；空字符串=清空密钥（停用签名）
	Enabled       *bool     `json:"enabled"`
}

// list 订阅列表（signing_secret 不回显，Sensitive 字段序列化时忽略）。
//
// @Summary      出站 webhook 订阅列表
// @Description  按 team 数据隔离返回动态出站订阅；signing_secret 不回显。
// @Tags         webhook
// @Produce      json
// @Success      200  {array}   ent.WebhookSubscription
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-subscriptions [get]
func (h *SubscriptionHandler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.WebhookSubscription.Query()
	// SEC-01 list 数据隔离：team 级用户仅见本团队归属的订阅；org 级全可见。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.WebhookSubscription{})
				}
				q = q.Where(webhooksubscription.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	subs, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, subs)
}

// create 创建订阅（明文 signing_secret 加密后落库）。
//
// @Summary      创建出站 webhook 订阅
// @Tags         webhook
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "订阅（含明文 signing_secret，加密存储）"
// @Success      201  {object} ent.WebhookSubscription
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-subscriptions [post]
func (h *SubscriptionHandler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.URL == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "url required"})
	}
	if e := h.checkCreateAccess(c, req.TeamID); e != nil {
		return e
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	b := h.db.WebhookSubscription.Create().
		SetURL(req.URL).
		SetEnabled(enabled)
	if req.Name != "" {
		b.SetName(req.Name)
	}
	if len(req.EventTypes) > 0 {
		b.SetEventTypes(req.EventTypes)
	}
	if req.SigningSecret != "" {
		enc, err := h.encryptSecret(req.SigningSecret)
		if err != nil {
			return errs.Internal(c, nil, err) // 加密失败（不含明文）
		}
		req.SigningSecret = "" // 明文用完即弃
		b.SetSigningSecret(enc)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	sub, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "webhook_subscription", "subscription already exists")
	}
	h.auditChange(c, auth.ActionWebhookSubscriptionCreate, sub)
	return c.JSON(http.StatusCreated, sub)
}

// get 订阅详情（signing_secret 不回显）。
//
// @Summary      出站 webhook 订阅详情
// @Tags         webhook
// @Produce      json
// @Param        id   path      int  true  "订阅 ID"
// @Success      200  {object} ent.WebhookSubscription
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-subscriptions/{id} [get]
func (h *SubscriptionHandler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermWebhookSubscriptionView); e != nil {
		return e
	}
	sub, err := h.db.WebhookSubscription.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "webhook subscription not found"})
	}
	return c.JSON(http.StatusOK, sub)
}

// update 更新订阅（传 signing_secret 则重加密替换；空字符串清空签名）。
//
// @Summary      更新出站 webhook 订阅
// @Tags         webhook
// @Accept       json
// @Produce      json
// @Param        id    path      int        true  "订阅 ID"
// @Param        body  body      updateReq  true  "更新字段"
// @Success      200  {object} ent.WebhookSubscription
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-subscriptions/{id} [patch]
func (h *SubscriptionHandler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermWebhookSubscriptionUpdate); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.WebhookSubscription.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.URL != nil {
		if *req.URL == "" {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "url cannot be empty"})
		}
		u.SetURL(*req.URL)
	}
	if req.EventTypes != nil {
		u.SetEventTypes(*req.EventTypes)
	}
	if req.Enabled != nil {
		u.SetEnabled(*req.Enabled)
	}
	if req.SigningSecret != nil {
		if *req.SigningSecret == "" {
			u.ClearSigningSecret() // 空字符串=停用签名
		} else {
			enc, err := h.encryptSecret(*req.SigningSecret)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			*req.SigningSecret = "" // 明文用完即弃
			u.SetSigningSecret(enc)
		}
	}
	sub, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "webhook_subscription")
	}
	h.auditChange(c, auth.ActionWebhookSubscriptionUpdate, sub)
	return c.JSON(http.StatusOK, sub)
}

// delete 删除订阅。
//
// @Summary      删除出站 webhook 订阅
// @Tags         webhook
// @Param        id   path      int  true  "订阅 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-subscriptions/{id} [delete]
func (h *SubscriptionHandler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermWebhookSubscriptionDelete); e != nil {
		return e
	}
	victim, _ := h.db.WebhookSubscription.Get(c.Request().Context(), id)
	if err := h.db.WebhookSubscription.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	if victim == nil {
		victim = &ent.WebhookSubscription{ID: id}
	}
	h.auditChange(c, auth.ActionWebhookSubscriptionDelete, victim)
	return c.NoContent(http.StatusNoContent)
}

// encryptSecret 加密 signing_secret（cipher 为 nil 时按明文存储，降级/单测）。
func (h *SubscriptionHandler) encryptSecret(plaintext string) (string, error) {
	if h.cipher == nil {
		return plaintext, nil
	}
	return h.cipher.Encrypt(plaintext)
}
