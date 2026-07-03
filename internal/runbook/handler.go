// handler.go Runbook API（CRUD + 触发执行）。
package runbook

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler Runbook API。
type Handler struct {
	db     *ent.Client
	engine *Engine
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建 Runbook handler。
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

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 runbook 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "runbook", id)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if !allowed {
		return errs.Forbidden(c, "")
	}
	return nil
}

// Register 挂载路由（鉴权中间件由装配方按需添加）。
func (h *Handler) Register(g *echo.Group) {
	g.GET("/runbooks", h.list)
	g.POST("/runbooks", h.create)
	g.GET("/runbooks/:id", h.get)
	g.PATCH("/runbooks/:id", h.update)
	g.DELETE("/runbooks/:id", h.delete)
	g.POST("/runbooks/:id/execute", h.execute)
}

// ListRunbooks 列出全部 Runbook。
//
// @Summary      List runbooks
// @Description  返回全部 Runbook（无分页）。
// @Tags         runbook
// @Produce      json
// @Success      200  {array}  ent.Runbook
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /runbooks [get]
// @Security     bearerAuth
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Runbook.Query()
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
					return c.JSON(http.StatusOK, []*ent.Runbook{})
				}
				q = q.Where(entrunbook.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	rbs, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, rbs)
}

type createReq struct {
	Name            string               `json:"name"`
	Type            string               `json:"type"` // document | executable
	ContentMarkdown string               `json:"content_markdown"`
	Trigger         map[string]any       `json:"trigger"`
	Steps           []schema.RunbookStep `json:"steps"`
}

// CreateRunbook 创建 Runbook。
//
// @Summary      Create runbook
// @Description  新建 Runbook（文档型或可执行型，含 trigger 与 steps）。
// @Tags         runbook
// @Accept       json
// @Produce      json
// @Param        request  body      createReq  true  "Runbook 定义"
// @Success      201      {object}  ent.Runbook
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /runbooks [post]
// @Security     bearerAuth
func (h *Handler) create(c *echo.Context) error {
	var req createReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	// QA 审计 C4 数据层兜底：写步骤（Readonly=false）必须 RequireApproval=true，
	// 防止通过配置绕过 engine 的"写操作必须 approved"安全控制。
	if err := validateSteps(req.Steps); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	rb := h.db.Runbook.Create().SetName(req.Name).SetType(entrunbook.Type(req.Type))
	if req.ContentMarkdown != "" {
		rb.SetContentMarkdown(req.ContentMarkdown)
	}
	if req.Trigger != nil {
		rb.SetTrigger(req.Trigger)
	}
	if len(req.Steps) > 0 {
		rb.SetSteps(req.Steps)
	}
	saved, err := rb.Save(c.Request().Context())
	if err != nil {
		return errs.FailConstraint(c, nil, err, "runbook", "runbook already exists")
	}
	return c.JSON(http.StatusCreated, saved)
}

// GetRunbook 获取单个 Runbook。
//
// @Summary      Get runbook
// @Description  按 ID 取得 Runbook。
// @Tags         runbook
// @Produce      json
// @Param        id   path      int  true  "Runbook ID"
// @Success      200  {object}  ent.Runbook
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Router       /runbooks/{id} [get]
// @Security     bearerAuth
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermRunbookView); e != nil {
		return e
	}
	rb, err := h.db.Runbook.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	return c.JSON(http.StatusOK, rb)
}

// updateReq 更新 Runbook 请求（全可选指针，PATCH 部分更新语义）。
type updateReq struct {
	Name            *string               `json:"name"`
	Type            *string               `json:"type"` // document | executable
	ContentMarkdown *string               `json:"content_markdown"`
	Trigger         map[string]any        `json:"trigger"`
	Steps           *[]schema.RunbookStep `json:"steps"`
}

// update 更新 Runbook。
//
// @Summary      Update runbook
// @Description  按 ID 更新 Runbook（部分字段，PATCH 语义）。
// @Tags         runbook
// @Accept       json
// @Produce      json
// @Param        id       path      int          true  "Runbook ID"
// @Param        request  body      updateReq    true  "更新字段（全可选）"
// @Success      200      {object}  ent.Runbook
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      404      {object}  httputil.ErrorResponse
// @Router       /runbooks/{id} [patch]
// @Security     bearerAuth
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermRunbookView); e != nil {
		return e
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	u := h.db.Runbook.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Type != nil {
		u.SetType(entrunbook.Type(*req.Type))
	}
	if req.ContentMarkdown != nil {
		u.SetContentMarkdown(*req.ContentMarkdown)
	}
	if req.Trigger != nil {
		u.SetTrigger(req.Trigger)
	}
	if req.Steps != nil {
		// QA 审计 C4 数据层兜底：写步骤必须 RequireApproval=true。
		if err := validateSteps(*req.Steps); err != nil {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
		}
		u.SetSteps(*req.Steps)
	}
	rb, err := u.Save(c.Request().Context())
	if err != nil {
		return errs.FailNotFound(c, nil, err, "runbook")
	}
	return c.JSON(http.StatusOK, rb)
}

// DeleteRunbook 删除 Runbook。
//
// @Summary      Delete runbook
// @Description  按 ID 删除 Runbook。
// @Tags         runbook
// @Param        id   path  int  true  "Runbook ID"
// @Success      204
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /runbooks/{id} [delete]
// @Security     bearerAuth
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermRunbookView); e != nil {
		return e
	}
	if err := h.db.Runbook.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// executeReq 触发执行请求。
type executeReq struct {
	IncidentID int  `json:"incident_id"`
	Approved   bool `json:"approved"` // 写动作是否已确认（human-in-the-loop）
}

// ExecuteRunbook 触发执行 Runbook。
//
// @Summary      Execute runbook
// @Description  按 incident 触发 Runbook 执行（approved=false 时跳过写动作，human-in-the-loop）。
// @Tags         runbook
// @Accept       json
// @Produce      json
// @Param        id       path      int          true  "Runbook ID"
// @Param        request  body      executeReq   true  "执行参数（incident_id + approved）"
// @Success      200      {object}  runbook.ExecuteResult
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /runbooks/{id}/execute [post]
// @Security     bearerAuth
func (h *Handler) execute(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermRunbookExecute); e != nil {
		return e
	}
	var req executeReq
	_ = c.Bind(&req) // approved 可缺省（默认 false，写动作会被跳过）

	res, err := h.engine.Execute(c.Request().Context(), id, req.IncidentID, req.Approved)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, res)
}

// validateSteps 数据层兜底校验（QA 审计 C4）：
// 写步骤（target.readonly=false）必须 require_approval=true。
// 防止通过 API 配置成"写操作不需确认"绕过 engine 的强制 approved 控制。
func validateSteps(steps []schema.RunbookStep) error {
	for _, s := range steps {
		if !s.Action.Target.Readonly && !s.RequireApproval {
			return fmt.Errorf("step %q is a write action (readonly=false) and must set require_approval=true", s.Name)
		}
	}
	return nil
}
