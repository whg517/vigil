// Package service 实现服务目录 API（能力域 4/13 服务管理）。
//
// Service 是路由的锚点、软隔离的核心载体（ADR-0013 / ADR-0028）。
// 此前 Service 仅有 ent schema 无 HTTP handler，本包补 list/get/create/update/delete。
//
// 权限点 service.* 由调用方在装配时按角色授权（与 auth.Handler 一致）。
package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entservice "github.com/kevin/vigil/ent/service"
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

// Handler 服务目录 API。
type Handler struct {
	db    *ent.Client
	authz *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建服务目录 handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
}

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 service 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "service", id)
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
//	GET    /services
//	POST   /services
//	GET    /services/:id
//	PATCH  /services/:id
//	DELETE /services/:id
func (h *Handler) Register(g *echo.Group) {
	g.GET("/services", h.list)
	g.POST("/services", h.create)
	g.GET("/services/:id", h.get)
	g.PATCH("/services/:id", h.update)
	g.DELETE("/services/:id", h.delete)
	// T6.2 服务拓扑（M4.4）：一层依赖查询——本服务依赖谁（depends_on）+ 谁依赖本服务（dependents，影响面）。
	g.GET("/services/:id/dependencies", h.dependencies)
	// N2.4 服务拓扑完整影响面（M4.4）：传递闭包——递归展开上游影响面 + 下游依赖链，带环检测。
	g.GET("/services/:id/impact", h.impact)
}

// list 服务目录列表。
//
// @Summary      服务列表
// @Tags         service
// @Produce      json
// @Param        source  query    string  false  "按来源筛选：manual | auto（治理自动供给的服务）"
// @Success      200  {array}   ent.Service
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Service.Query()
	// source 过滤（治理：只看自动供给 / 只看手工）。非法值返 400，避免静默全量误导。
	if src := c.QueryParam("source"); src != "" {
		switch entservice.Source(src) {
		case entservice.SourceManual, entservice.SourceAuto:
			q = q.Where(entservice.SourceEQ(entservice.Source(src)))
		default:
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid source filter"})
		}
	}
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
					return c.JSON(http.StatusOK, []*ent.Service{})
				}
				q = q.Where(entservice.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	svcs, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, svcs)
}

// createReq 创建服务请求。
type createReq struct {
	Name               string            `json:"name"`
	Slug               string            `json:"slug"`
	Description        string            `json:"description"`
	Labels             map[string]string `json:"labels"`
	AutoCreateIncident *bool             `json:"auto_create_incident"`
	Status             string            `json:"status"` // active | disabled
	TeamID             int               `json:"team_id"`
	EscalationPolicyID int               `json:"escalation_policy_id"` // 可选，关联升级策略
	// ScheduleIDs / RunbookIDs 关联排班/处置手册（M4.5 继承源）。
	// Service 是配置枢纽：路由命中后 Incident 继承 Service 的升级策略、排班、处置手册。
	// 此处仅暴露「配置入口」，让 schema 已有的边可经 API 建立；创建时全量设置。
	ScheduleIDs []int `json:"schedule_ids"`
	RunbookIDs  []int `json:"runbook_ids"`
	// DependsOnIDs 本服务依赖的下游服务 id（T6.2/M4.4 服务拓扑）。
	// 仅存依赖关系，供影响面分析基础；创建时全量设置。
	DependsOnIDs []int `json:"depends_on_ids"`
}

// create 创建服务。
//
// @Summary      创建服务
// @Tags         service
// @Accept       json
// @Produce      json
// @Param        body  body     createReq  true  "服务创建参数"
// @Success      201   {object} ent.Service
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services [post]
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" || req.Slug == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name and slug required"})
	}
	b := h.db.Service.Create().
		SetName(req.Name).
		SetSlug(req.Slug)
	if req.Description != "" {
		b.SetDescription(req.Description)
	}
	if req.Labels != nil {
		b.SetLabels(req.Labels)
	}
	if req.AutoCreateIncident != nil {
		b.SetAutoCreateIncident(*req.AutoCreateIncident)
	}
	if req.Status != "" {
		b.SetStatus(entservice.Status(req.Status))
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if req.EscalationPolicyID > 0 {
		b.SetEscalationPolicyID(req.EscalationPolicyID)
	}
	// 关联排班/处置手册（去重防重复添加导致的唯一约束冲突）。
	if ids := dedupIDs(req.ScheduleIDs); len(ids) > 0 {
		b.AddScheduleIDs(ids...)
	}
	if ids := dedupIDs(req.RunbookIDs); len(ids) > 0 {
		b.AddRunbookIDs(ids...)
	}
	// 服务依赖（去重 + 剔除自引用，防止服务依赖自己形成无意义自环）。
	if ids := filterSelf(dedupIDs(req.DependsOnIDs), 0); len(ids) > 0 {
		b.AddDependsOnIDs(ids...)
	}
	s, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "service", "service slug already exists")
	}
	// 回带关联 id：前端配置页需知道当前关联了哪些排班/手册。
	return c.JSON(http.StatusCreated, h.withAssociations(c.Request().Context(), s))
}

// get 服务详情。
//
// @Summary      服务详情
// @Tags         service
// @Produce      json
// @Param        id   path     int  true  "服务 ID"
// @Success      200  {object} ent.Service
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	s, err := h.db.Service.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, h.withAssociations(c.Request().Context(), s))
}

// updateReq 更新服务请求（全部字段可选，部分更新）。
type updateReq struct {
	Name               *string            `json:"name"`
	Slug               *string            `json:"slug"`
	Description        *string            `json:"description"`
	Labels             *map[string]string `json:"labels"`
	AutoCreateIncident *bool              `json:"auto_create_incident"`
	Status             *string            `json:"status"`
	// EscalationPolicyID 关联升级策略。指针区分三种语义：
	//   nil  —— 不修改（请求未带该字段）
	//   0   —— 解除关联（显式清空）
	//   >0  —— 关联指定策略
	EscalationPolicyID *int `json:"escalation_policy_id"`
	// ScheduleIDs / RunbookIDs 关联排班/处置手册，**全量替换**语义（指针区分）：
	//   nil     —— 不修改（请求未带该字段）
	//   []      —— 清空全部关联（显式传空数组）
	//   [x,y]   —— 替换为指定集合（先清后加，最终关联即此集合）
	ScheduleIDs *[]int `json:"schedule_ids"`
	RunbookIDs  *[]int `json:"runbook_ids"`
	// DependsOnIDs 服务依赖，全量替换语义（同 ScheduleIDs）：nil 不改 / [] 清空 / [x,y] 替换。
	DependsOnIDs *[]int `json:"depends_on_ids"`
	// Source 转正（方案C §3.5 治理）：仅接受 "manual"，把自动供给的服务标记为手工管理，
	// 使其脱离自动供给的治理范畴（不再被过期清理/主动同步触碰）。不接受 "auto"（不能人为伪造自动来源）。
	Source *string `json:"source"`
}

// update 更新服务。
//
// @Summary      更新服务
// @Tags         service
// @Accept       json
// @Produce      json
// @Param        id    path     int        true  "服务 ID"
// @Param        body  body     updateReq  true  "服务更新参数"
// @Success      200   {object} ent.Service
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	// 关联排班/处置手册/服务依赖 + 转正是「配置枢纽/治理」写操作，须 service.update（仅 view 的只读角色不得改）。
	if req.ScheduleIDs != nil || req.RunbookIDs != nil || req.DependsOnIDs != nil || req.Source != nil {
		if e := h.checkAccess(c, id, auth.PermServiceUpdate); e != nil {
			return e
		}
	}
	// 转正：仅允许标记为 manual（不能人为伪造 auto 来源）。非法值 400。
	if req.Source != nil && *req.Source != string(entservice.SourceManual) {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "source can only be set to manual (adopt)"})
	}
	upd := h.db.Service.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Slug != nil {
		upd.SetSlug(*req.Slug)
	}
	if req.Description != nil {
		upd.SetDescription(*req.Description)
	}
	if req.Labels != nil {
		upd.SetLabels(*req.Labels)
	}
	if req.AutoCreateIncident != nil {
		upd.SetAutoCreateIncident(*req.AutoCreateIncident)
	}
	if req.Source != nil {
		// 已在上方校验只可能是 manual。转正后保留 provisioned_at 作历史痕迹。
		upd.SetSource(entservice.SourceManual)
	}
	if req.Status != nil {
		upd.SetStatus(entservice.Status(*req.Status))
	}
	// 升级策略关联：nil 不改，0 解绑，>0 关联。
	if req.EscalationPolicyID != nil {
		if *req.EscalationPolicyID > 0 {
			upd.SetEscalationPolicyID(*req.EscalationPolicyID)
		} else {
			upd.ClearEscalationPolicy()
		}
	}
	// 排班关联：全量替换（先清后加）。nil 不改，[] 清空，[x,y] 替换为该集合。
	if req.ScheduleIDs != nil {
		upd.ClearSchedules()
		if ids := dedupIDs(*req.ScheduleIDs); len(ids) > 0 {
			upd.AddScheduleIDs(ids...)
		}
	}
	// 处置手册关联：同上全量替换。
	if req.RunbookIDs != nil {
		upd.ClearRunbooks()
		if ids := dedupIDs(*req.RunbookIDs); len(ids) > 0 {
			upd.AddRunbookIDs(ids...)
		}
	}
	// 服务依赖：同上全量替换，且剔除自引用（服务不能依赖自己）。
	if req.DependsOnIDs != nil {
		upd.ClearDependsOn()
		if ids := filterSelf(dedupIDs(*req.DependsOnIDs), id); len(ids) > 0 {
			upd.AddDependsOnIDs(ids...)
		}
	}
	s, err := upd.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, h.withAssociations(c.Request().Context(), s))
}

// serviceResponse 服务响应体：内嵌 ent.Service 全字段，附带关联的排班/处置手册 id。
//
// 背景：Service↔Schedule/Runbook 是多对多边，ent.Service 默认序列化不含边数据；
// 前端配置页需要「当前关联了哪些排班/手册」的 id 列表来回显与增删，故显式回带。
type serviceResponse struct {
	*ent.Service
	ScheduleIDs  []int `json:"schedule_ids"`
	RunbookIDs   []int `json:"runbook_ids"`
	DependsOnIDs []int `json:"depends_on_ids"` // 本服务依赖的下游服务 id（T6.2 服务拓扑）
}

// withAssociations 为 Service 查出其关联的 schedule/runbook/depends_on id 并包装为响应体。
// 查询失败时降级为空列表（不阻断主响应，仅关联回显缺失）。
func (h *Handler) withAssociations(ctx context.Context, s *ent.Service) serviceResponse {
	resp := serviceResponse{Service: s, ScheduleIDs: []int{}, RunbookIDs: []int{}, DependsOnIDs: []int{}}
	if ids, err := s.QuerySchedules().IDs(ctx); err == nil {
		resp.ScheduleIDs = ids
	}
	if ids, err := s.QueryRunbooks().IDs(ctx); err == nil {
		resp.RunbookIDs = ids
	}
	if ids, err := s.QueryDependsOn().IDs(ctx); err == nil {
		resp.DependsOnIDs = ids
	}
	return resp
}

// dedupIDs 去重并剔除非正 id，避免重复关联触发唯一约束冲突或关联到无效 id。
func dedupIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// filterSelf 剔除等于 self 的 id（防止服务依赖自己形成无意义自环）。self<=0 时不过滤。
func filterSelf(ids []int, self int) []int {
	if self <= 0 || len(ids) == 0 {
		return ids
	}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id == self {
			continue
		}
		out = append(out, id)
	}
	return out
}

// dependencyNode 依赖拓扑节点（精简回显，只需 id/name/slug/status 供前端画图/跳转）。
type dependencyNode struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// dependenciesResp 一层依赖查询响应（T6.2/M4.4 服务拓扑基础）。
type dependenciesResp struct {
	ServiceID  int              `json:"service_id"`
	DependsOn  []dependencyNode `json:"depends_on"` // 本服务依赖的下游服务
	Dependents []dependencyNode `json:"dependents"` // 依赖本服务的上游服务（本服务故障的影响面）
}

// dependencies 返回服务的一层依赖拓扑：depends_on（依赖谁）+ dependents（谁依赖它=影响面）。
//
// 仅做一层查询（轻量、供拓扑图直连边渲染）。递归展开完整传递闭包 + 环检测见 GET /services/:id/impact。
//
// @Summary      服务依赖拓扑（一层）
// @Tags         service
// @Produce      json
// @Param        id   path     int  true  "服务 ID"
// @Success      200  {object} dependenciesResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id}/dependencies [get]
func (h *Handler) dependencies(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	ctx := c.Request().Context()
	s, err := h.db.Service.Get(ctx, id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	resp := dependenciesResp{ServiceID: id, DependsOn: []dependencyNode{}, Dependents: []dependencyNode{}}
	downstream, err := s.QueryDependsOn().All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	for _, d := range downstream {
		resp.DependsOn = append(resp.DependsOn, toNode(d))
	}
	upstream, err := s.QueryDependents().All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	for _, u := range upstream {
		resp.Dependents = append(resp.Dependents, toNode(u))
	}
	return c.JSON(http.StatusOK, resp)
}

// toNode 把 ent.Service 精简为拓扑节点。
func toNode(s *ent.Service) dependencyNode {
	return dependencyNode{ID: s.ID, Name: s.Name, Slug: s.Slug, Status: s.Status.String()}
}

// maxTopologyDepth 传递闭包 BFS 的最大遍历深度（防超大依赖图拖垮请求）。
// 一层邻接查询一次 DB，深度即最坏 DB 往返次数；到限即安全截断（truncated 标记）。
const maxTopologyDepth = 20

// impactNode 影响面节点：在拓扑节点基础上附带距起点的层级 depth（1=直接邻接，2=间接…）。
type impactNode struct {
	dependencyNode
	Depth int `json:"depth"` // 距起点的最短跳数（BFS 层级），供前端按影响半径分层展示
}

// impactResp 传递影响面响应（N2.4/M4.4）。
type impactResp struct {
	ServiceID int `json:"service_id"`
	// UpstreamImpact 本服务故障时递归受影响的上游（dependents 传递闭包）——核心影响面。
	UpstreamImpact []impactNode `json:"upstream_impact"`
	// DownstreamDeps 本服务递归依赖的下游（depends_on 传递闭包）——排障时的连带排查面。
	DownstreamDeps []impactNode `json:"downstream_deps"`
	// CycleDetected 依赖图存在环时置 true（BFS 靠 visited 集合安全终止，不死循环）。
	CycleDetected bool `json:"cycle_detected"`
	// Truncated 遍历触达 maxTopologyDepth 被截断时置 true（超大图保护，结果可能不完整）。
	Truncated bool `json:"truncated"`
}

// impact 返回服务的完整传递影响面（N2.4/M4.4 服务拓扑）。
//
// 相比一层的 /dependencies，本端点做 BFS 传递闭包：
//   - upstream_impact：递归展开 dependents（谁依赖本服务→本服务故障连带影响谁）。
//   - downstream_deps：递归展开 depends_on（本服务依赖谁→排障时连带排查谁）。
//
// 环检测：依赖图可能有环（A→B→A，或配置错误引入的环），BFS 用 visited 集合去重，
// 命中已访问节点即跳过（不死循环），并置 cycle_detected=true 提示图中存在环。
// 深度限制：遍历超过 maxTopologyDepth 层即截断并置 truncated（超大图保护）。
//
// @Summary      服务传递影响面（完整拓扑）
// @Description  BFS 传递闭包：upstream_impact=递归上游影响面，downstream_deps=递归下游依赖；带环检测与深度限制。
// @Tags         service
// @Produce      json
// @Param        id   path     int  true  "服务 ID"
// @Success      200  {object} impactResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id}/impact [get]
func (h *Handler) impact(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	ctx := c.Request().Context()
	// 先确认起点服务存在（不存在返 404，与 dependencies 一致）。
	if _, err := h.db.Service.Get(ctx, id); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	resp := impactResp{ServiceID: id, UpstreamImpact: []impactNode{}, DownstreamDeps: []impactNode{}}

	// 上游影响面：沿 dependents 边（谁依赖本服务）递归。
	up, upCycle, upTrunc, err := h.transitiveClosure(ctx, id, func(s *ent.Service) *ent.ServiceQuery {
		return s.QueryDependents()
	})
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	resp.UpstreamImpact = up

	// 下游依赖链：沿 depends_on 边（本服务依赖谁）递归。
	down, downCycle, downTrunc, err := h.transitiveClosure(ctx, id, func(s *ent.Service) *ent.ServiceQuery {
		return s.QueryDependsOn()
	})
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	resp.DownstreamDeps = down

	resp.CycleDetected = upCycle || downCycle
	resp.Truncated = upTrunc || downTrunc
	return c.JSON(http.StatusOK, resp)
}

// transitiveClosure 从 startID 出发沿 neighbors 指定的边做 BFS 传递闭包。
//
// 返回：按 BFS 层级带 depth 的节点列表（不含起点自身）、是否检测到环、是否因深度限制被截断。
// 环检测：visited 集合记录已入队节点（含起点）；再次遇到已访问节点即跳过并标记 cycle
// （安全终止不死循环）。深度限制：超过 maxTopologyDepth 层即停止扩展并标记 truncated。
func (h *Handler) transitiveClosure(
	ctx context.Context,
	startID int,
	neighbors func(*ent.Service) *ent.ServiceQuery,
) (nodes []impactNode, cycle bool, truncated bool, err error) {
	nodes = []impactNode{}
	visited := map[int]bool{startID: true} // 起点已访问，防自环把自己算进影响面
	frontier := []int{startID}
	for depth := 1; len(frontier) > 0; depth++ {
		if depth > maxTopologyDepth {
			// 仍有未展开的前沿却已达深度上限：结果不完整，安全截断。
			truncated = true
			break
		}
		var next []int
		for _, cur := range frontier {
			s, gerr := h.db.Service.Get(ctx, cur)
			if gerr != nil {
				if ent.IsNotFound(gerr) {
					continue // 边指向的服务已被删（悬空），跳过
				}
				return nil, false, false, gerr
			}
			adj, qerr := neighbors(s).All(ctx)
			if qerr != nil {
				return nil, false, false, qerr
			}
			for _, n := range adj {
				if visited[n.ID] {
					// 已访问过（含回到起点）：图中存在环，跳过防死循环。
					cycle = true
					continue
				}
				visited[n.ID] = true
				nodes = append(nodes, impactNode{dependencyNode: toNode(n), Depth: depth})
				next = append(next, n.ID)
			}
		}
		frontier = next
	}
	return nodes, cycle, truncated, nil
}

// delete 删除服务。
//
// @Summary      删除服务
// @Tags         service
// @Param        id   path  int  true  "服务 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /services/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermServiceView); e != nil {
		return e
	}
	if err := h.db.Service.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}
