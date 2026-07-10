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
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"
	"github.com/kevin/vigil/internal/ingestion"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行写操作，造成"报 403 却已落库"的越权。故 checkAccess 拒绝时返回
// 本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// tokenPrefix 接入 token 前缀（防与 API Key 混淆）。
const tokenPrefix = "vig_int_"

// Handler 接入点管理 API。
type Handler struct {
	db       *ent.Client
	authz    *auth.Authorizer           // 资源级鉴权（SEC-01，可选注入）
	scope    *auth.ScopeResolver        // 资源→team 反查（SEC-01，可选注入）
	audit    *auth.AuditRecorder        // 配置变更留痕（C21，可选注入，nil 时跳过）
	adapters *ingestion.AdapterRegistry // 干跑测试用（T5.1，可选注入，nil 时 test 端点降级）
}

// NewHandler 创建接入点 handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
}

// SetAdapterRegistry 注入归一化适配器注册表（T5.1：/integrations/:id/test 干跑用同一适配器）。
func (h *Handler) SetAdapterRegistry(r *ingestion.AdapterRegistry) { h.adapters = r }

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（C21：接入点配置变更留痕，main 装配时调用）。
func (h *Handler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// auditConfigChange 记录接入点配置变更审计（C21）。
// 接入点是告警源接入面，创建/改动/删除都留痕（含 who + 对象 id/name）。
func (h *Handler) auditConfigChange(c *echo.Context, action string, integ *ent.Integration) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
	e.Action = action
	e.ResourceType = "integration"
	e.ResourceID = integ.ID
	e.ResourceName = integ.Name
	h.audit.MustRecord(c.Request().Context(), e)
}

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 integration 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "integration", id)
	if err != nil {
		// errs.Internal 写完 500 返回 nil，必须换成非 nil 哨兵，否则调用方不会中止。
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		// 同理：errs.Forbidden 写完 403 返回 nil，返回哨兵让调用方 return 中止后续写操作。
		_ = errs.Forbidden(c, "")
		return errAccessDenied
	}
	return nil
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
	// 接入运维（T5.1）：干跑测试（不建单）+ token 轮换（旧失效）。
	g.POST("/integrations/:id/test", h.test)
	g.POST("/integrations/:id/rotate-token", h.rotateToken)
	// T6.2/M14.6 集成向导后端辅助：配置模板/接线指引（只读，无副作用，任何登录用户可查）。
	g.GET("/integrations/config-template", h.configTemplate)
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
	Type      string         `json:"type"`   // webhook|email|prometheus|grafana|api
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
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Integration.Query()
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// org 级用户（orgWide）全可见；team 级用户仅可见 binding 的 team；无 binding 返回空。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, []*ent.Integration{})
				}
				q = q.Where(integration.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	ints, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
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
func (h *Handler) create(c *echo.Context) error {
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
		return errs.FailConstraint(c, nil, err, "integration", "integration already exists")
	}
	h.auditConfigChange(c, auth.ActionIntegrationCreate, integ)
	return c.JSON(http.StatusCreated, createResp{Integration: integ, Token: integ.Token})
}

// integrationDetail 详情视图（含明文 token，供已授权用户查看接入 URL/token）。
//
// 安全说明：webhook token 是 URL 路径密钥（webhook 端点 POST /api/v1/webhook/<token>），
// 非加密凭据、可安全回显；本端点已按 integration.view 鉴权（且 list 数据隔离按可见 team 过滤），
// 授权 admin 查看自己接入点的 token（等同展示 webhook URL）合理且必要。
// 仅在详情（单个 :id）返回 token，list 保持不回显——避免批量暴露。
type integrationDetail struct {
	*ent.Integration
	Token string `json:"token"` // webhook 鉴权 token（URL 路径密钥，详情持久展示用）
}

// get 接入点详情（含 webhook 鉴权 token，供表单持久展示接入 URL/token）。
//
// @Summary      接入点详情
// @Tags         integration
// @Produce      json
// @Param        id   path      int  true  "接入点 ID"
// @Success      200  {object} integrationDetail
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
	}
	integ, err := h.db.Integration.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "integration not found"})
	}
	return c.JSON(http.StatusOK, integrationDetail{Integration: integ, Token: integ.Token})
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
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
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
		return errs.FailNotFound(c, nil, err, "integration")
	}
	h.auditConfigChange(c, auth.ActionIntegrationUpdate, integ)
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
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIntegrationView); e != nil {
		return e
	}
	// 删除前取快照：审计要记对象名，删掉后就查不到了。取不到（已不存在）用零值兜底。
	victim, _ := h.db.Integration.Get(c.Request().Context(), id)
	if err := h.db.Integration.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	if victim == nil {
		victim = &ent.Integration{ID: id}
	}
	h.auditConfigChange(c, auth.ActionIntegrationDelete, victim)
	return c.NoContent(http.StatusNoContent)
}

// testReq 干跑测试请求：样例 payload（原始告警字节，走该接入点 type 的适配器归一化）。
type testReq struct {
	// Payload 样例告警 payload（原样透传给适配器）。为空时用最小占位（便于快速验证适配器可加载）。
	Payload json.RawMessage `json:"payload"`
}

// testResp 干跑响应：归一化预览（不建单，不落库）。
type testResp struct {
	// Matched 归一化是否成功（适配器解析通过）。
	Matched bool `json:"matched"`
	// Count 归一化产出的 Event 条数（一次 payload 可能含多条 alert）。
	Count int `json:"count"`
	// Events 归一化预览（severity/status/labels 等，供验证 labels 命中/路由是否如预期）。
	Events []testEventPreview `json:"events"`
	// Error 归一化失败原因（Matched=false 时非空）。
	Error string `json:"error,omitempty"`
}

// testEventPreview 单条归一化预览（对齐 NormalizedEvent 关键字段，供人工核对）。
type testEventPreview struct {
	SourceEventID string            `json:"source_event_id"`
	Source        string            `json:"source"`
	Severity      string            `json:"severity"`
	Status        string            `json:"status"`
	Summary       string            `json:"summary"`
	Labels        map[string]string `json:"labels,omitempty"`
	DedupKey      string            `json:"dedup_key"`
}

// test 干跑测试接入点（T5.1）：样例 payload 走归一化验证，返回预览，不真建单/不落库。
//
// 用途：接入配置后先验证 payload 能被正确归一化（severity/status/labels 命中），
// 再正式接告警源，避免"接上才发现字段映射错、路由标签不命中"。
//
// @Summary      接入点干跑测试
// @Description  用样例 payload 走该接入点的归一化适配器，返回归一化预览（labels/severity 等），不建单不落库。
// @Tags         integration
// @Accept       json
// @Produce      json
// @Param        id    path      int      true  "接入点 ID"
// @Param        body  body      testReq  true  "样例 payload"
// @Success      200  {object} testResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id}/test [post]
func (h *Handler) test(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 干跑属接入运维，需对接入点 integration.update 权限（团队软隔离）。
	if e := h.checkAccess(c, id, auth.PermIntegrationUpdate); e != nil {
		return e
	}
	integ, err := h.db.Integration.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "integration not found"})
	}
	var req testReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if h.adapters == nil {
		return errs.Internal(c, nil, nil, "adapter registry not configured")
	}
	adapter, ok := h.adapters.Get(integ.Type.String())
	if !ok {
		return c.JSON(http.StatusOK, testResp{Matched: false, Error: "no adapter for integration type " + integ.Type.String()})
	}
	payload := []byte(req.Payload)
	if len(payload) == 0 {
		payload = []byte("{}") // 空 payload 占位，验证适配器可加载（多数适配器会因缺字段返错，正是预期反馈）。
	}
	// 干跑：只调 Normalize，绝不落 RawEvent/Event/建单——纯预览。
	evts, nerr := adapter.Normalize(c.Request().Context(), payload, integ, nil)
	if nerr != nil {
		return c.JSON(http.StatusOK, testResp{Matched: false, Error: nerr.Error()})
	}
	preview := make([]testEventPreview, 0, len(evts))
	for _, e := range evts {
		preview = append(preview, testEventPreview{
			SourceEventID: e.SourceEventID,
			Source:        e.Source,
			Severity:      e.Severity,
			Status:        e.Status,
			Summary:       e.Summary,
			Labels:        e.Labels,
			DedupKey:      e.DedupKey,
		})
	}
	return c.JSON(http.StatusOK, testResp{Matched: true, Count: len(evts), Events: preview})
}

// rotateTokenResp 轮换响应（含新 token，仅本次返回一次）。
type rotateTokenResp struct {
	ID    int    `json:"id"`
	Token string `json:"token"` // ★ 新明文 token，仅轮换时返回一次；旧 token 即失效
}

// rotateToken 轮换接入点 webhook 鉴权 token（T5.1）：生成新 token，旧 token 立即失效。
//
// 用途：token 疑似泄露 / 定期轮换凭据。轮换后旧 token 的 webhook 请求将鉴权失败（401），
// 接入方须换用新 token。是重置凭据的高危动作，须留痕（integration.rotate_token）。
//
// @Summary      轮换接入 token
// @Description  生成新的 webhook 鉴权 token，旧 token 立即失效。新 token 仅本次返回一次。
// @Tags         integration
// @Produce      json
// @Param        id   path      int  true  "接入点 ID"
// @Success      200  {object} rotateTokenResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/{id}/rotate-token [post]
func (h *Handler) rotateToken(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 轮换凭据需 integration.update 权限（团队软隔离）。
	if e := h.checkAccess(c, id, auth.PermIntegrationUpdate); e != nil {
		return e
	}
	newToken := generateToken()
	integ, err := h.db.Integration.UpdateOneID(id).SetToken(newToken).Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "integration")
	}
	// 轮换留痕（凭据重置高危动作）。
	if h.audit != nil {
		e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
		e.Action = auth.ActionIntegrationRotateToken
		e.ResourceType = "integration"
		e.ResourceID = integ.ID
		e.ResourceName = integ.Name
		h.audit.MustRecord(c.Request().Context(), e)
	}
	return c.JSON(http.StatusOK, rotateTokenResp{ID: integ.ID, Token: newToken})
}
