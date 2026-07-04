// handler.go 订阅管理 API（T4.4）：登录用户管理自己的定向订阅。
//
// 端点（均作用于「当前登录用户自己的」订阅，不涉他人）：
//   - GET    /subscriptions            列出我的订阅
//   - POST   /subscriptions            新建订阅（订阅一个 team 或 service）
//   - DELETE /subscriptions/:id         删除我的某条订阅
//
// 鉴权模型：订阅是「用户对自己的偏好设置」，不需要额外权限点——任何登录用户都能管理自己的订阅。
// 但订阅的 scope（team/service）必须是该用户「可见」的（有 org 级绑定 or 该 team 的 team 级绑定），
// 避免用户订阅自己无权查看的团队/服务（否则等于绕过软隔离窥探其他团队的 Incident 摘要）。
package subscription

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entservice "github.com/kevin/vigil/ent/service"
	entsubscription "github.com/kevin/vigil/ent/subscription"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 订阅管理 API。
type Handler struct {
	db    *ent.Client
	authz *auth.Authorizer // 可见性校验（订阅 scope 必须对用户可见），可选注入
}

// NewHandler 创建订阅 handler。
func NewHandler(db *ent.Client) *Handler { return &Handler{db: db} }

// SetAuthorizer 注入鉴权器（订阅 scope 可见性校验）。为 nil 时降级放行（渐进/单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// Register 挂载路由到业务路由组（v1，已过 RequireUser 身份解析）。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/subscriptions", h.list)
	g.POST("/subscriptions", h.create)
	g.DELETE("/subscriptions/:id", h.delete)
}

// createReq 创建订阅请求。team_id 与 service_id 二选一（恰好一个 > 0）。
type createReq struct {
	TeamID      int      `json:"team_id"`      // 订阅的团队（与 service_id 二选一）
	ServiceID   int      `json:"service_id"`   // 订阅的服务（与 team_id 二选一）
	Channels    []string `json:"channels"`     // 通道偏好（有序降级链），空=默认链
	MinSeverity string   `json:"min_severity"` // 最低告知严重度（critical|warning|info），空=warning
}

// list 列出当前用户的订阅。
//
// @Summary      我的订阅列表
// @Tags         subscription
// @Produce      json
// @Success      200  {array}   ent.Subscription
// @Failure      401  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /subscriptions [get]
func (h *Handler) list(c *echo.Context) error {
	uid, ok := auth.UserIDFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
	}
	subs, err := h.db.Subscription.Query().
		Where(entsubscription.HasUserWith(user.IDEQ(uid))).
		WithTeam().
		WithService().
		Order(ent.Desc(entsubscription.FieldCreatedAt)).
		All(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if subs == nil {
		subs = []*ent.Subscription{}
	}
	return c.JSON(http.StatusOK, subs)
}

// create 新建订阅（订阅一个 team 或 service）。
//
// @Summary      新建订阅
// @Tags         subscription
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "订阅配置"
// @Success      201  {object} ent.Subscription
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /subscriptions [post]
func (h *Handler) create(c *echo.Context) error {
	ctx := c.Request().Context()
	uid, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
	}
	var req createReq
	if err := c.Bind(&req); err != nil {
		return errs.BadRequest(c, "invalid body")
	}
	// scope 二选一：恰好一个 > 0。
	hasTeam := req.TeamID > 0
	hasService := req.ServiceID > 0
	if hasTeam == hasService {
		return errs.BadRequest(c, "exactly one of team_id or service_id is required")
	}
	minSev := req.MinSeverity
	if minSev == "" {
		minSev = "warning"
	}
	if _, valid := severityRank[minSev]; !valid {
		return errs.BadRequest(c, "invalid min_severity (want critical|warning|info)")
	}

	// 可见性校验：订阅 scope 必须对用户可见（防越权窥探其他团队/服务的 Incident 摘要）。
	// 解算目标 team（service 订阅反查其归属 team），与用户可见 team 比对。
	targetTeamID, err := h.scopeTeamID(ctx, req.TeamID, req.ServiceID)
	if err != nil {
		return errs.BadRequest(c, "team or service not found")
	}
	if e := h.checkVisible(c, uid, targetTeamID); e != nil {
		return e
	}

	b := h.db.Subscription.Create().
		SetUserID(uid).
		SetMinSeverity(entsubscription.MinSeverity(minSev))
	if len(req.Channels) > 0 {
		b.SetChannels(req.Channels)
	}
	if hasTeam {
		b.SetTeamID(req.TeamID)
	} else {
		b.SetServiceID(req.ServiceID)
	}
	sub, err := b.Save(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	// 回带边（team/service）便于前端展示。
	sub, _ = h.db.Subscription.Query().
		Where(entsubscription.IDEQ(sub.ID)).
		WithTeam().WithService().Only(ctx)
	return c.JSON(http.StatusCreated, sub)
}

// delete 删除当前用户的一条订阅（只能删自己的）。
//
// @Summary      删除订阅
// @Tags         subscription
// @Param        id   path      int  true  "订阅 ID"
// @Success      204
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /subscriptions/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	ctx := c.Request().Context()
	uid, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "not authenticated"})
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 只能删自己的：按 (id, user) 双条件删——他人的订阅匹配不到，返 404（不泄露存在性）。
	n, err := h.db.Subscription.Delete().
		Where(
			entsubscription.IDEQ(id),
			entsubscription.HasUserWith(user.IDEQ(uid)),
		).
		Exec(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if n == 0 {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "subscription not found"})
	}
	return c.NoContent(http.StatusNoContent)
}

// scopeTeamID 解算订阅 scope 归属的 team id（team 订阅即其自身；service 订阅反查其归属 team）。
// 目标 team/service 不存在返回 error。service 无 team 归属返回 (0, nil)（org 级服务，交可见性校验兜底）。
func (h *Handler) scopeTeamID(ctx context.Context, teamID, serviceID int) (int, error) {
	if teamID > 0 {
		if _, err := h.db.Team.Get(ctx, teamID); err != nil {
			return 0, err
		}
		return teamID, nil
	}
	svc, err := h.db.Service.Query().Where(entservice.IDEQ(serviceID)).WithTeam().Only(ctx)
	if err != nil {
		return 0, err
	}
	if svc.Edges.Team != nil {
		return svc.Edges.Team.ID, nil
	}
	return 0, nil // service 无 team 归属
}

// checkVisible 校验目标 team 对用户可见（org 级用户全可见；team 级用户须持该 team 的绑定）。
// authz 未注入时降级放行（渐进/单测）。
func (h *Handler) checkVisible(c *echo.Context, uid, targetTeamID int) error {
	if h.authz == nil {
		return nil
	}
	visible, orgWide, err := h.authz.VisibleTeamIDs(c.Request().Context(), uid)
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if orgWide {
		return nil
	}
	// 无 team 归属的 org 级 service：仅 org 级用户可订阅。
	if targetTeamID <= 0 {
		_ = errs.Forbidden(c, "cannot subscribe to a resource outside your visible teams")
		return errAccessDenied
	}
	for _, id := range visible {
		if id == targetTeamID {
			return nil
		}
	}
	_ = errs.Forbidden(c, "cannot subscribe to a resource outside your visible teams")
	return errAccessDenied
}

// errAccessDenied 哨兵错误：checkVisible 已写出响应，handler 立即 return 中止后续逻辑
// （与 ticket/postmortem handler 同款，errs.Forbidden 写完响应返回 nil，须换非 nil 哨兵防越权落库）。
var errAccessDenied = errors.New("access denied (response already written)")
