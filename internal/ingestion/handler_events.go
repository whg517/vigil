// handler_events.go 开放 API 事件投递端点（T5.1，能力域 01 §5 / 10 §A3）。
//
// POST /api/v1/events —— 外部系统程序化投递 Event。
//
// 与 webhook（POST /webhook/:token）的区别：
//   - webhook：接入点用 token 自证身份（公开路由，不走 RBAC），告警源直连。
//   - 开放 API：登录态 / API Key（X-Vigil-Key）用户主动投递，走 v1 RBAC 链路，
//     须 event.create 权限 + 对目标接入点的团队软隔离（不能凭 API Key 往任意接入点灌）。
//
// 两者最终共用同一 ingest 核心（落 RawEvent → 限流/背压 → 归一化队列 → 分诊），
// payload 走目标接入点 type 对应的适配器归一化（通用 JSON 接入点用 GenericJSONAdapter）。
// 幂等由归一化落 Event 的 (source, source_event_id, status) 唯一索引保证——重复 source_event_id
// 不产新 Event（与 webhook 完全一致）。
package ingestion

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
// 与 integration/triage handler 同款——errs.Forbidden/Internal 写完响应返回 nil，
// 若直接透传则调用方不会中止，会在已写 403 情况下继续执行投递写操作，造成"报 403 却已投递"。
var errAccessDenied = errors.New("access denied (response already written)")

// SetAuthorizer 注入鉴权器（开放 API 目标接入点资源级鉴权，T5.1）。
// 为 nil 时降级放行（渐进/单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// RegisterOpenAPI 挂载开放 API 事件投递路由到受保护的 v1 组。
//
//	POST /events   开放 API 程序化投递 Event（X-Vigil-Key 鉴权，走同一分诊链路）
func (h *Handler) RegisterOpenAPI(g *echo.Group) {
	g.POST("/events", h.deliverEvent)
}

// deliverEventReq 开放 API 投递请求：指定目标接入点 + 原始 payload（透传给适配器归一化）。
type deliverEventReq struct {
	// IntegrationID 目标接入点 ID。决定用哪个适配器归一化 + 事件归属哪个 team/service。
	IntegrationID int `json:"integration_id"`
}

// actorFromContext 取当前操作人 ID（鉴权中间件注入，X-Vigil-Key 解析出的 user）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkIntegrationAccess 校验当前用户对目标接入点是否有 event.create 权限（团队软隔离）。
// authz/scope 为 nil 时放行（渐进/单测）。
func (h *Handler) checkIntegrationAccess(c *echo.Context, integID int) error {
	if h.authz == nil || h.scope == nil {
		return nil
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope,
		h.actorFromContext(c), auth.PermEventCreate, "integration", integID)
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

// deliverEvent 开放 API 投递 Event（T5.1）。
//
// 与 webhook 同一分诊链路：读 body（原始 payload）→ 校验目标接入点 → ingest（落库/限流/入队/归一化）。
// 幂等：重复 source_event_id 经归一化唯一索引不产新 Event；限流/背压 payload 仍落库不丢。
//
// integration_id 从 query 参数或 body 顶层取（query 优先，便于把 payload 原样透传给适配器）。
// 若从 body 取，payload 里的 integration_id 字段对通用适配器无害（不是约定字段）。
//
// @Summary      开放 API 投递 Event
// @Description  外部系统凭 X-Vigil-Key 程序化投递告警。payload 走目标接入点适配器归一化，与 webhook 同一分诊链路。幂等（重复 source_event_id 不产新 Event），返回 202 + raw_event_id。
// @Tags         ingestion
// @Accept       json
// @Produce      json
// @Param        integration_id  query    int     false  "目标接入点 ID（未传则取 body.integration_id）"
// @Param        body            body     object  true   "原始告警 payload（透传给适配器归一化）"
// @Success      202  {object} httputil.AckResponse
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      429  {object} httputil.AckResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Failure      503  {object} httputil.AckResponse
// @Security     bearerAuth
// @Router       /events [post]
func (h *Handler) deliverEvent(c *echo.Context) error {
	ctx := c.Request().Context()

	// 读原始 payload（限制大小，防滥用；与 webhook 同上限）。
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxPayloadBytes))
	if err != nil {
		return errs.BadRequest(c, "read body: "+err.Error())
	}

	// 解析目标接入点 ID：query 优先（透传 body 给适配器），否则从 body 顶层解析。
	integID := resolveIntegrationID(c, body)
	if integID <= 0 {
		return errs.BadRequest(c, "integration_id required (query or body)")
	}

	// 校验接入点存在 + 启用（禁用接入点不接受投递，与 webhook 一致——见 C10）。
	integ, err := h.db.Integration.Get(ctx, integID)
	if err != nil {
		if ent.IsNotFound(err) {
			return errs.NotFound(c, "integration not found")
		}
		return errs.Internal(c, nil, err)
	}
	if !integ.Enabled {
		// 与 webhook 禁用推送同档：统一 401（C10——不区分"不存在/未启用"，防探测）。
		return errs.Unauthorized(c, "integration disabled")
	}

	// 资源级鉴权：对目标接入点需 event.create（团队软隔离，防凭 API Key 往任意接入点灌告警）。
	if e := h.checkIntegrationAccess(c, integID); e != nil {
		return e
	}

	// 共用 ingest 核心（落 RawEvent → 限流/背压 → 归一化队列 → 分诊）。
	ack, status := h.ingest(c, integ, body)
	return c.JSON(status, ack)
}

// resolveIntegrationID 从 query（优先）或 body 顶层 integration_id 字段解析目标接入点 ID。
// query 优先：便于把整段 body 原样透传给通用适配器归一化（不污染 payload）。
func resolveIntegrationID(c *echo.Context, body []byte) int {
	if q := c.QueryParam("integration_id"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			return n
		}
	}
	// 回退：从 body 顶层解析（用轻量结构，不消费/改动 body——ingest 仍用原始 body）。
	var req deliverEventReq
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	return req.IntegrationID
}
