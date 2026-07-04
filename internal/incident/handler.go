// handler.go Incident API（能力域 14 集成 + 8 IM/Web 操作入口）。
//
// 暴露 incident 查询与操作，是 IM 卡片/Web/外部系统的统一入口。
// 复用 incident.Service（含 ack/resolve/escalate + 时间线记录 + 升级取消）。
package incident

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"
	"github.com/kevin/vigil/internal/notification"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把
// 该 nil 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，
// handler 会在已写 403 的情况下继续执行写操作（ack/resolve/escalate/reopen），造成
// "报 403 却已落库"的越权。故 checkAccess 拒绝时返回本哨兵（非 nil），调用方据此中止；
// 响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler Incident API。
type Handler struct {
	db      *ent.Client
	svc     *Service
	actions *ActionRecorder     // 操作审计查询（GET /incidents/:id/actions，可选注入）
	authz   *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope   *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建 incident handler。
func NewHandler(db *ent.Client, svc *Service) *Handler {
	return &Handler{db: db, svc: svc, actions: NewActionRecorder(db)}
}

// SetActionRecorder 注入操作审计查询器（可选；未注入时 NewHandler 已按 db 兜底构造）。
func (h *Handler) SetActionRecorder(a *ActionRecorder) { h.actions = a }

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权 + list 数据隔离）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// Register 挂载路由。
// GET    /incidents           列表（?status=&severity=&limit=&offset=）
// GET    /incidents/:id       详情
// POST   /incidents/:id/ack   确认
// POST   /incidents/:id/resolve 解决
// POST   /incidents/:id/escalate 升级
func (h *Handler) Register(g *echo.Group) {
	g.GET("/incidents", h.list)
	g.GET("/incidents/:id", h.get)
	g.GET("/incidents/:id/actions", h.listActions)
	g.GET("/incidents/:id/notifications", h.listNotifications)
	g.POST("/incidents/:id/ack", h.ack)
	g.POST("/incidents/:id/resolve", h.resolve)
	g.POST("/incidents/:id/close", h.close)
	g.POST("/incidents/:id/escalate", h.escalate)
	g.POST("/incidents/:id/reopen", h.reopen)
	g.POST("/incidents/:id/skip-postmortem", h.skipPostmortem) // T4.1 显式跳过复盘闸门
}

// sourceFromRequest 从请求判定动作来源（C30 归因）。
//
// 复用 auth 身份解析同款依据（resolver.go）：
//   - X-Vigil-Key 头存在 → 外部程序化接入（API Key），source=api
//   - 否则 → Web/浏览器（Bearer JWT 或匿名渐进阶段），source=web
//
// IM 来源不经此 handler（IM 回调走 im.Handler，直接以 SourceIM 调 Service），
// 故这里只需区分 web 与 api，消除原「操作端点硬编码 web」把 API Key 调用误记为 web 的归因错误。
func (h *Handler) sourceFromRequest(c *echo.Context) Source {
	if c.Request().Header.Get("X-Vigil-Key") != "" {
		return SourceAPI
	}
	return SourceWeb
}

// list 查询事件列表（?status=&severity=&limit=&offset=）。
// total 与筛选条件一致（用 clone 在加 limit/offset 前统计）。
//
// @Summary      查询事件列表
// @Description  按状态/严重度过滤并分页返回事件，total 与筛选条件一致。
// @Tags         incident
// @Produce      json
// @Param        status    query    string  false  "按状态过滤"  Enums(triggered, escalated, acked, resolved, closed)
// @Param        severity  query    string  false  "按严重度过滤"  Enums(critical, warning, info)
// @Param        limit     query    int     false  "分页大小（默认 50，上限 200）"  default(50) maximum(200)
// @Param        offset    query    int     false  "分页偏移"  default(0)
// @Success      200       {object} httputil.Paginated[ent.Incident]
// @Failure      500       {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Incident.Query()
	if s := c.QueryParam("status"); s != "" {
		q = q.Where(incident.StatusEQ(incident.Status(s)))
	}
	if s := c.QueryParam("severity"); s != "" {
		q = q.Where(incident.SeverityEQ(incident.Severity(s)))
	}
	// T4.1 待复盘可见性：?pending_postmortem=true 过滤「resolved 后停在待复盘」的 critical 单——
	// 即 severity=critical + status=resolved + 未跳过复盘 + 尚无 published/archived 复盘。
	// 让运营/团队能一眼看到「已解决但复盘未收口」的欠账，配合闸门推动复盘闭环。
	if pp := c.QueryParam("pending_postmortem"); pp == "true" {
		q = q.Where(
			incident.SeverityEQ(incident.SeverityCritical),
			incident.StatusEQ(incident.StatusResolved),
			incident.PostmortemSkipped(false),
			incident.Not(incident.HasPostmortemWith(
				postmortem.StatusIn(postmortem.StatusPublished, postmortem.StatusArchived),
			)),
		)
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
					return c.JSON(http.StatusOK, httputil.Paginated[*ent.Incident]{Items: []*ent.Incident{}, Total: 0})
				}
				q = q.Where(incident.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	// 在加 limit/offset 前 clone 出计数 query，保证 total 与列表筛选条件一致
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q = q.Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	items, err := q.Order(ent.Desc(incident.FieldCreatedAt)).All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.Incident]{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// get 事件详情（含 responders/events）。
//
// @Summary      事件详情
// @Description  含 responders 与 events 关联。
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// SEC-01：资源级鉴权——反查 incident.team → 校验当前用户对该 team 的 view 权限。
	uid := h.actorFromContext(c)
	if allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, uid, auth.PermIncidentView, "incident", id); err != nil {
		return errs.Internal(c, nil, err)
	} else if !allowed {
		return errs.Forbidden(c, "")
	}
	inc, err := h.db.Incident.Query().
		Where(incident.IDEQ(id)).
		WithResponders().
		WithEvents().
		Only(c.Request().Context())
	if err != nil {
		return errs.NotFound(c, "not found")
	}
	return c.JSON(http.StatusOK, inc)
}

// listActions 查询某事件的操作审计（IncidentAction，按时间升序，分页）。
//
// 与时间线（GET /incidents/:id/timeline）互补：时间线是全程可读留痕，
// 本端点是结构化处置动作审计（who/via/type），供审计视图与 IM-first 渠道统计用。
//
// @Summary      查询事件操作审计
// @Description  返回对该事件的处置动作审计（ack/resolve/escalate/reopen/close/add_responder），含 via 渠道与操作人，按时间升序分页。
// @Tags         incident
// @Produce      json
// @Param        id      path   int  true   "事件 ID"
// @Param        limit   query  int  false  "分页大小（默认 100，上限 500）"
// @Param        offset  query  int  false  "分页偏移"
// @Success      200  {object} httputil.Paginated[ent.IncidentAction]
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/actions [get]
func (h *Handler) listActions(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 与时间线查询同权限：incident.view（能读事件即可读其操作审计）。
	if e := h.checkAccess(c, id, auth.PermIncidentView); e != nil {
		return e
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	items, err := h.actions.QueryActions(c.Request().Context(), id, limit, offset)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	total, _ := h.actions.CountActions(c.Request().Context(), id)
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.IncidentAction]{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// listNotifications 查询某事件的通知送达记录（Notification，按时间升序，分页）。
//
// 与操作审计（/actions）互补：actions 是「谁做了什么处置」，本端点是「通知发给了谁、
// 走哪个通道、送达/失败/被静默」的送达账本（B22/M13），供夜间打扰/送达率等指标与排障用。
//
// @Summary      查询事件通知送达记录
// @Description  返回对该事件的通知送达记录（sent/failed/suppressed/pending），含通道/目标/原因/层级，按时间升序分页。
// @Tags         incident
// @Produce      json
// @Param        id      path   int  true   "事件 ID"
// @Param        limit   query  int  false  "分页大小（默认 100，上限 500）"
// @Param        offset  query  int  false  "分页偏移"
// @Success      200  {object} httputil.Paginated[ent.Notification]
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/notifications [get]
func (h *Handler) listNotifications(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 与时间线/操作审计同权限：incident.view（能读事件即可读其送达记录）。
	if e := h.checkAccess(c, id, auth.PermIncidentView); e != nil {
		return e
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	items, total, err := notification.QueryByIncident(c.Request().Context(), h.db, id, limit, offset)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.Notification]{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
// 对应 CLAUDE.md 边界：不绕过 RBAC，actor 不再来自请求 body（防伪造）。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 incident 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "incident", id)
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

// ack 确认事件（@Router /incidents/{id}/ack）。
//
// @Summary      确认事件（ack）
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/ack [post]
func (h *Handler) ack(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIncidentAck); e != nil {
		return e
	}
	inc, err := h.svc.Ack(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		// B25 归一：不存在的 id → 404 not_found；状态非法（ErrInvalidTransition）→ 400 failed_precondition。
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}

// resolve 解决事件。
//
// @Summary      解决事件（resolve）
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/resolve [post]
func (h *Handler) resolve(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIncidentResolve); e != nil {
		return e
	}
	inc, err := h.svc.Resolve(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		// B25 归一：不存在 → 404；状态非法 → 400 failed_precondition。
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}

// close 关闭事件（推进到终态 closed）。
//
// @Summary      关闭事件（close）
// @Description  把 resolved 事件推进到终态 closed；非 resolved 状态（含 triggered/acked/escalated）关闭返回 400。
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/close [post]
func (h *Handler) close(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIncidentClose); e != nil {
		return e
	}
	inc, err := h.svc.Close(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		// 已 closed 幂等：不当失败——直接回读当前单据以 200 返回，让重复点击/并发关闭表现一致。
		if errors.Is(err, ErrAlreadyClosed) {
			cur, gerr := h.db.Incident.Get(c.Request().Context(), id)
			if gerr != nil {
				return errs.FailActionState(c, nil, gerr, "incident")
			}
			return c.JSON(http.StatusOK, cur)
		}
		// T4.1 复盘闸门：critical 未完成复盘不可 close → 400 failed_precondition + 明确提示
		// （先完成复盘发布或显式跳过），前端据此引导用户走复盘/跳过路径。
		if errors.Is(err, ErrPostmortemRequired) {
			return errs.BadRequestWith(c, "failed_precondition",
				"critical 事件须先完成复盘（发布）或显式跳过复盘后才能关闭")
		}
		// B25 归一：不存在 → 404；状态非法（如 triggered 直接 close）→ 400 failed_precondition。
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}

// escalate 升级事件。
//
// @Summary      升级事件（escalate）
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/escalate [post]
func (h *Handler) escalate(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIncidentEscalate); e != nil {
		return e
	}
	inc, err := h.svc.Escalate(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		// B25 归一：不存在 → 404；状态非法 → 400 failed_precondition。
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}

// reopen 重新打开已解决/已关闭的事件。
//
// @Summary      重新打开事件（reopen）
// @Description  把 resolved/closed 事件回退为 triggered（待响应），清空 resolved_at。
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/reopen [post]
func (h *Handler) reopen(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermIncidentReopen); e != nil {
		return e
	}
	inc, err := h.svc.Reopen(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		// B25 归一：不存在 → 404；状态非法 → 400 failed_precondition。
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}

// skipPostmortem 显式跳过复盘闸门（T4.1）。
//
// critical 事件 resolved 后受复盘闸门约束（未完成复盘不可 close）。确无复盘必要（误报/演练）时，
// 有权者可显式跳过，置 postmortem_skipped=true 放行后续 close，而非强制走复盘。
// 权限：postmortem.update——跳过复盘是复盘治理决策，须有复盘管理权（非仅 incident.close）。
//
// @Summary      跳过复盘（skip postmortem gate）
// @Description  置 postmortem_skipped=true，放行 critical 事件在未完成复盘时 close。
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/skip-postmortem [post]
func (h *Handler) skipPostmortem(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// 资源级鉴权：跳过复盘属复盘治理写操作，须 postmortem.update；经 incident→team 回溯归属。
	if e := h.checkAccess(c, id, auth.PermPostmortemUpdate); e != nil {
		return e
	}
	inc, err := h.svc.SkipPostmortem(c.Request().Context(), id, h.actorFromContext(c), h.sourceFromRequest(c))
	if err != nil {
		return errs.FailActionState(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, inc)
}
