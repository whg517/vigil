// handler.go Postmortem/ActionItem API（能力域 12）。
package postmortem

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/actionitem"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 复盘 API。
type Handler struct {
	db     *ent.Client
	engine *Engine
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建复盘 handler。
func NewHandler(db *ent.Client, e *Engine) *Handler {
	return &Handler{db: db, engine: e}
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

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 kind 资源是否有 perm 权限。
// kind 取值：postmortem / action_item（间接归属：ScopeResolver 多级回溯到 incident.team）。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission, kind string) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, kind, id)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if !allowed {
		return errs.Forbidden(c, "")
	}
	return nil
}

// Register 挂载路由。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/postmortems", h.list)
	g.GET("/postmortems/:id", h.get)
	g.POST("/incidents/:id/postmortem/draft", h.generateDraft) // 为事件生成草稿
	g.PATCH("/postmortems/:id/transition", h.transition)       // 状态流转
	g.DELETE("/postmortems/:id", h.delete)                     // 删除复盘
	// ActionItem
	g.POST("/postmortems/:id/action-items", h.addActionItem)
	g.PATCH("/action-items/:id", h.updateActionItem)
	g.DELETE("/action-items/:id", h.deleteActionItem) // 删除改进项
}

// list 复盘列表（含 incident 关联）。
//
// @Summary      复盘列表
// @Tags         postmortem
// @Produce      json
// @Success      200  {array}   ent.Postmortem
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /postmortems [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Postmortem.Query()
	// SEC-01 list 数据隔离：按当前用户可见 team 过滤。
	// postmortem 间接归属 team（经 incident），用 HasIncidentWith + incident.HasTeamWith 组合 join。
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
					return c.JSON(http.StatusOK, []any{})
				}
				q = q.Where(postmortem.HasIncidentWith(incident.HasTeamWith(team.IDIn(teamIDs...))))
			}
		}
	}
	pms, err := q.WithIncident().All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, flattenAll(pms))
}

// get 复盘详情（含 incident + action-items 关联）。
//
// @Summary      复盘详情
// @Tags         postmortem
// @Produce      json
// @Param        id   path     int  true  "复盘 ID"
// @Success      200  {object} ent.Postmortem
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /postmortems/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermPostmortemView, "postmortem"); e != nil {
		return e
	}
	pm, err := h.db.Postmortem.Query().
		Where(postmortem.IDEQ(id)).
		WithIncident().
		WithActionItems().
		Only(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	return c.JSON(http.StatusOK, flatten(pm))
}

// generateDraft 为事件生成复盘草稿（AI + 时间线）。
//
// @Summary      生成复盘草稿
// @Description  基于时间线与（可选）AI 生成事件复盘草稿。
// @Tags         postmortem
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      201  {object} ent.Postmortem
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/postmortem/draft [post]
func (h *Handler) generateDraft(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid incident id"})
	}
	pm, err := h.engine.GenerateDraft(c.Request().Context(), incID)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, flatten(pm))
}

// transitionReq 状态流转请求。
type transitionReq struct {
	Status string `json:"status"` // in_review | published | archived
}

// transition 复盘状态流转。
//
// @Summary      复盘状态流转
// @Tags         postmortem
// @Accept       json
// @Produce      json
// @Param        id    path     int             true  "复盘 ID"
// @Param        body  body     transitionReq   true  "目标状态"
// @Success      200   {object} ent.Postmortem
// @Failure      400   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /postmortems/{id}/transition [patch]
func (h *Handler) transition(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermPostmortemView, "postmortem"); e != nil {
		return e
	}
	var req transitionReq
	if err := c.Bind(&req); err != nil || req.Status == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "status required"})
	}
	pm, err := h.engine.Transition(c.Request().Context(), id, postmortem.Status(req.Status))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, flatten(pm))
}

// addActionItemReq 添加改进项。
type addActionItemReq struct {
	Description string `json:"description"`
	OwnerID     string `json:"owner_id"`
	TrackerURL  string `json:"tracker_url"`
}

// addActionItem 添加改进项。
//
// @Summary      添加改进项
// @Tags         postmortem
// @Accept       json
// @Produce      json
// @Param        id    path     int               true  "复盘 ID"
// @Param        body  body     addActionItemReq  true  "改进项参数"
// @Success      201   {object} ent.ActionItem
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /postmortems/{id}/action-items [post]
func (h *Handler) addActionItem(c *echo.Context) error {
	pmID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// id 参数为 postmortem id；checkAccess 按 postmortem 间接归属回溯到 incident.team。
	if e := h.checkAccess(c, pmID, auth.PermPostmortemActionItemManage, "postmortem"); e != nil {
		return e
	}
	var req addActionItemReq
	if err := c.Bind(&req); err != nil || req.Description == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "description required"})
	}
	ai, err := h.db.ActionItem.Create().
		SetDescription(req.Description).
		SetOwnerID(req.OwnerID).
		SetTrackerURL(req.TrackerURL).
		SetPostmortemID(pmID).
		Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, ai)
}

// updateActionItemReq 更新改进项（状态/负责人/工单）。
type updateActionItemReq struct {
	Status     *string `json:"status"` // open | in_progress | done
	OwnerID    *string `json:"owner_id"`
	TrackerURL *string `json:"tracker_url"`
}

// updateActionItem 更新改进项。
//
// @Summary      更新改进项
// @Tags         postmortem
// @Accept       json
// @Produce      json
// @Param        id    path     int                 true  "改进项 ID"
// @Param        body  body     updateActionItemReq true  "更新字段（全部可选）"
// @Success      200   {object} ent.ActionItem
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /action-items/{id} [patch]
func (h *Handler) updateActionItem(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// id 参数为 action_item id；ScopeResolver 经 action_item→postmortem→incident→team 三级回溯。
	if e := h.checkAccess(c, id, auth.PermPostmortemActionItemManage, "action_item"); e != nil {
		return e
	}
	var req updateActionItemReq
	_ = c.Bind(&req)

	update := h.db.ActionItem.UpdateOneID(id)
	if req.Status != nil {
		update.SetStatus(actionitem.Status(*req.Status))
	}
	if req.OwnerID != nil {
		update.SetOwnerID(*req.OwnerID)
	}
	if req.TrackerURL != nil {
		update.SetTrackerURL(*req.TrackerURL)
	}
	ai, err := update.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, ai)
}

// delete 删除复盘（连同其改进项一起删除，避免孤儿/外键冲突）。
//
// @Summary      删除复盘
// @Description  按 ID 删除复盘，并级联删除其关联的改进项。
// @Tags         postmortem
// @Param        id   path  int  true  "复盘 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /postmortems/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermPostmortemView, "postmortem"); e != nil {
		return e
	}
	ctx := c.Request().Context()
	// 先删关联改进项，再删复盘（无 OnDelete 声明，避免外键约束/孤儿）。
	if _, derr := h.db.ActionItem.Delete().Where(actionitem.HasPostmortemWith(postmortem.ID(id))).Exec(ctx); derr != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: derr.Error()})
	}
	if err := h.db.Postmortem.DeleteOneID(id).Exec(ctx); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// deleteActionItem 删除单个改进项。
//
// @Summary      删除改进项
// @Description  按 ID 删除改进项。
// @Tags         postmortem
// @Param        id   path  int  true  "改进项 ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /action-items/{id} [delete]
func (h *Handler) deleteActionItem(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// id 参数为 action_item id；ScopeResolver 经 action_item→postmortem→incident→team 三级回溯。
	if e := h.checkAccess(c, id, auth.PermPostmortemActionItemManage, "action_item"); e != nil {
		return e
	}
	if err := h.db.ActionItem.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}
