// handler.go 分诊路由 API（能力域 3-4）。
//
// 暴露未路由（unrouted）Event 的人工重路由端点（M6）：
//
//	POST /events/:id/reroute   把 unrouted Event 指派到指定 Service（body: {service_id}）
//
// 权限：对目标 Service 需 service.route_override（团队软隔离——只能指派到有权限的 Service），
// 由装配层在 RouteGuard 登记。资源级 scope 校验在 handler 内按目标 service 反查 team 完成。
package triage

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景与其它 handler 一致：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若直接透传
// 则 `if e := checkAccess(...); e != nil` 永不触发，会在已写 403 的情况下继续执行重路由写操作。
// 故拒绝时返回本哨兵（非 nil），调用方据此中止。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler 分诊路由 API。
type Handler struct {
	db     *ent.Client
	engine *Engine
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建分诊路由 handler。engine 用于执行重路由后的聚合/建单。
func NewHandler(db *ent.Client, engine *Engine) *Handler {
	return &Handler{db: db, engine: engine}
}

// SetAuthorizer 注入鉴权器（SEC-01：资源级鉴权）。为 nil 时降级放行（渐进/单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID（鉴权中间件注入）。未注入时返回 0。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkServiceAccess 校验当前用户对目标 Service 是否有 perm 权限（团队软隔离）。
// authz/scope 为 nil 时放行（渐进/单测）。
func (h *Handler) checkServiceAccess(c *echo.Context, serviceID int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "service", serviceID)
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

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	g.POST("/events/:id/reroute", h.reroute)
}

type rerouteReq struct {
	ServiceID int `json:"service_id"`
}

// RerouteEvent 把未路由 Event 人工指派到指定 Service（M6）。
//
// 仅作用于 unrouted Event（尚无 Service 归属）；已路由的 Event 返 409（改派既有单走
// incident.reassign）。指派后按目标 Service 立即聚合/建单，使误判未路由的告警进入处置流程。
//
// @Summary      Reroute unrouted event
// @Description  把未路由 Event 指派到指定 Service，并按该 Service 聚合/建单。
// @Tags         triage
// @Accept       json
// @Produce      json
// @Param        id       path   int          true  "Event ID"
// @Param        request  body   rerouteReq   true  "目标 service_id"
// @Success      200      {object}  map[string]any
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      403      {object}  httputil.ErrorResponse
// @Failure      404      {object}  httputil.ErrorResponse
// @Failure      409      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /events/{id}/reroute [post]
// @Security     bearerAuth
func (h *Handler) reroute(c *echo.Context) error {
	evtID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	var req rerouteReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.ServiceID <= 0 {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "service_id required"})
	}
	ctx := c.Request().Context()

	// 校验目标 Service 存在（也便于对不存在 service 返 404 而非落到引擎的模糊错误）。
	if _, err := h.db.Service.Get(ctx, req.ServiceID); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "service not found"})
		}
		return errs.Internal(c, nil, err)
	}
	// 权限：对目标 Service 需 service.route_override（团队软隔离——只能派给有权限的服务）。
	if e := h.checkServiceAccess(c, req.ServiceID, auth.PermServiceRouteOverride); e != nil {
		return e
	}

	res, err := h.engine.Reroute(ctx, evtID, req.ServiceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "event or service not found"})
		}
		if errors.Is(err, ErrRerouteAlreadyRouted) {
			return errs.Conflict(c, "event already routed to a service")
		}
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"action":       string(res.Action),
		"incident_id":  res.IncidentID,
		"incident_num": res.IncidentNum,
		"service_id":   res.ServiceID,
		"service_name": res.ServiceName,
	})
}
