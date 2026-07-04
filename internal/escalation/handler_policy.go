// handler_policy.go 升级策略管理 API（能力域 6 升级，PRD M6.x）。
//
// 此前 escalation 包只有 engine（被 triage 在内存调用），无 CRUD handler。
// 本文件补 list/get/create/update/delete，供前端管理升级层级配置。
//
// 注意：与 engine.go 的 HandleTask（Asynq 任务处理）区分，本文件是 HTTP CRUD。
package escalation

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/escalationpolicy"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行写操作，造成"报 403 却已落库"的越权。故 checkAccess 拒绝时返回
// 本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// PolicyHandler 升级策略管理 API（与处理升级任务的 Engine 区分）。
type PolicyHandler struct {
	db    *ent.Client
	authz *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewPolicyHandler 创建升级策略 handler。
func NewPolicyHandler(db *ent.Client) *PolicyHandler {
	return &PolicyHandler{db: db}
}

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *PolicyHandler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *PolicyHandler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
func (h *PolicyHandler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 escalation_policy 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *PolicyHandler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "escalation_policy", id)
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
//	GET    /escalation-policies
//	POST   /escalation-policies
//	GET    /escalation-policies/:id
//	PATCH  /escalation-policies/:id
//	DELETE /escalation-policies/:id
func (h *PolicyHandler) Register(g *echo.Group) {
	g.GET("/escalation-policies", h.list)
	g.POST("/escalation-policies", h.create)
	g.GET("/escalation-policies/:id", h.get)
	g.PATCH("/escalation-policies/:id", h.update)
	g.DELETE("/escalation-policies/:id", h.delete)
}

// createReq 创建升级策略请求。
type createReq struct {
	Name        string                   `json:"name"`
	RepeatTimes int                      `json:"repeat_times"`
	Levels      []schema.EscalationLevel `json:"levels"`
	// TeamID 归属团队（B26）：不设则为无主资源，team 级用户按 SEC-01 过滤后 list 看不到。
	// 非 org 级用户只能给自己可管的 team 建（经 VisibleTeamIDs 校验，否则 403）。
	TeamID int `json:"team_id"`
}

// checkCreateTeam 创建归属校验（B26）：非 org 级用户只能给自己可见/可管的 team 建资源。
//
// 返回 echo error 形式，非 nil 时 handler 直接 return（已写响应）。
// authz 未注入时放行（兼容渐进/单测）。org 级用户（orgWide）可给任意 team 建。
func (h *PolicyHandler) checkCreateTeam(c *echo.Context, teamID int) error {
	if h.authz == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	uid := h.actorFromContext(c)
	if uid <= 0 {
		return nil // 匿名/系统：不校验（与 list 一致）
	}
	teamIDs, orgWide, err := h.authz.VisibleTeamIDs(c.Request().Context(), uid)
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if orgWide {
		return nil // org 级：可给任意 team 建
	}
	// team 级：仅当 teamID 在可见集合内才放行。
	for _, id := range teamIDs {
		if id == teamID {
			return nil
		}
	}
	_ = errs.Forbidden(c, "无权在该团队下创建升级策略")
	return errAccessDenied
}

// list 升级策略列表。
//
// @Summary      升级策略列表
// @Tags         escalation
// @Produce      json
// @Success      200  {array}   ent.EscalationPolicy
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /escalation-policies [get]
func (h *PolicyHandler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.EscalationPolicy.Query()
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
					return c.JSON(http.StatusOK, []*ent.EscalationPolicy{})
				}
				q = q.Where(escalationpolicy.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	policies, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, policies)
}

// create 创建升级策略。
//
// @Summary      创建升级策略
// @Tags         escalation
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "策略配置"
// @Success      201  {object} ent.EscalationPolicy
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /escalation-policies [post]
func (h *PolicyHandler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	// B26：team 级用户只能给可管 team 建（否则建完自己都看不到，且越权占用别团队命名空间）。
	if req.TeamID > 0 {
		if e := h.checkCreateTeam(c, req.TeamID); e != nil {
			return e
		}
	}
	b := h.db.EscalationPolicy.Create().SetName(req.Name).SetRepeatTimes(req.RepeatTimes)
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	// levels 是必填 JSON 字段（无默认值），未传时须显式置空切片，
	// 否则 ent 校验报 missing required field 返 500（可先建策略、后补层级）。
	if req.Levels == nil {
		req.Levels = []schema.EscalationLevel{}
	}
	b.SetLevels(req.Levels)
	policy, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "escalation policy", "escalation policy already exists")
	}
	return c.JSON(http.StatusCreated, policy)
}

// get 升级策略详情。
//
// @Summary      升级策略详情
// @Tags         escalation
// @Produce      json
// @Param        id   path      int  true  "策略 ID"
// @Success      200  {object} ent.EscalationPolicy
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /escalation-policies/{id} [get]
func (h *PolicyHandler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermEscalationView); e != nil {
		return e
	}
	policy, err := h.db.EscalationPolicy.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "policy not found"})
	}
	return c.JSON(http.StatusOK, policy)
}

// updateReq 更新升级策略请求。
type updateReq struct {
	Name        *string                   `json:"name"`
	RepeatTimes *int                      `json:"repeat_times"`
	Levels      *[]schema.EscalationLevel `json:"levels"`
}

// update 更新升级策略。
//
// @Summary      更新升级策略
// @Tags         escalation
// @Accept       json
// @Produce      json
// @Param        id    path      int         true  "策略 ID"
// @Param        body  body      updateReq   true  "更新字段"
// @Success      200  {object} ent.EscalationPolicy
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /escalation-policies/{id} [patch]
func (h *PolicyHandler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermEscalationView); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.EscalationPolicy.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.RepeatTimes != nil {
		u.SetRepeatTimes(*req.RepeatTimes)
	}
	if req.Levels != nil {
		u.SetLevels(*req.Levels)
	}
	policy, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "escalation policy")
	}
	return c.JSON(http.StatusOK, policy)
}

// delete 删除升级策略。
//
// @Summary      删除升级策略
// @Tags         escalation
// @Param        id   path      int  true  "策略 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /escalation-policies/{id} [delete]
func (h *PolicyHandler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermEscalationView); e != nil {
		return e
	}
	if err := h.db.EscalationPolicy.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
