// handler.go Role/RoleBinding 管理 API（能力域 13 §4）。
//
// 角色与权限可由使用者自行配置管理（RBAC 自配置）：
// · Role CRUD（组合权限点，权限点须为合法枚举）
// · RoleBinding CRUD（把 Role 授予 User，带 org/team scope）
package auth

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v4"
)

// Handler RBAC 管理 API。
type Handler struct {
	db    *ent.Client
	audit *AuditRecorder // 审计记录器（可选，nil 时跳过审计）
}

// NewHandler 创建 RBAC handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
}

// SetAuditRecorder 注入审计记录器（main 装配时调用）。
func (h *Handler) SetAuditRecorder(r *AuditRecorder) {
	h.audit = r
}

// auditFrom 从请求构造审计条目（提取 actor + IP/UA）。
func (h *Handler) auditFrom(c echo.Context, action, resType string, resID int, resName string) AuditEntry {
	uid, _ := UserIDFromContext(c.Request().Context())
	e := AuditEntryFromRequest(c.Request(), uid, "")
	e.Action = action
	e.ResourceType = resType
	e.ResourceID = resID
	e.ResourceName = resName
	return e
}

// Register 挂载 RBAC 路由（这些路由本身需要 org_admin 权限，装配时加中间件）。
func (h *Handler) Register(g *echo.Group) {
	// Role
	g.GET("/roles", h.listRoles)
	g.POST("/roles", h.createRole)
	g.DELETE("/roles/:id", h.deleteRole)
	// RoleBinding
	g.GET("/role-bindings", h.listBindings)
	g.POST("/role-bindings", h.createBinding)
	g.DELETE("/role-bindings/:id", h.deleteBinding)
}

// ---- Role ----

// createRoleReq 创建角色请求体。
type createRoleReq struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScopeLevel  string   `json:"scope_level"` // org | team
	Permissions []string `json:"permissions"`
}

// listRoles 角色列表。
//
// @Summary      角色列表
// @Tags         rbac
// @Produce      json
// @Success      200  {array}   ent.Role
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /roles [get]
func (h *Handler) listRoles(c echo.Context) error {
	rls, err := h.db.Role.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, rls)
}

// createRole 创建角色。
//
// @Summary      创建角色
// @Tags         rbac
// @Accept       json
// @Produce      json
// @Param        body  body     createRoleReq  true  "角色创建参数"
// @Success      201   {object} ent.Role
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /roles [post]
func (h *Handler) createRole(c echo.Context) error {
	var req createRoleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	// 校验权限点合法性（角色配置时必须从系统枚举选）
	for _, p := range req.Permissions {
		if !Permission(p).IsValid() {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid permission: " + p})
		}
	}
	scope := role.ScopeLevel(req.ScopeLevel)
	if scope != role.ScopeLevelOrg && scope != role.ScopeLevelTeam {
		scope = role.ScopeLevelTeam // 缺省 team
	}
	rl, err := h.db.Role.Create().
		SetName(req.Name).
		SetDescription(req.Description).
		SetScopeLevel(scope).
		SetPermissions(req.Permissions).
		Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	if h.audit != nil {
		h.audit.MustRecord(c.Request().Context(), h.auditFrom(c, "role.create", "role", rl.ID, rl.Name))
	}
	return c.JSON(http.StatusCreated, rl)
}

// deleteRole 删除角色（内置角色不可删）。
//
// @Summary      删除角色
// @Tags         rbac
// @Param        id   path  int  true  "角色 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /roles/{id} [delete]
func (h *Handler) deleteRole(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	// 内置角色不可删（builtin=true）
	rl, err := h.db.Role.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "role not found"})
	}
	if rl.Builtin {
		return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: "builtin role cannot be deleted"})
	}
	if err := h.db.Role.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	if h.audit != nil {
		h.audit.MustRecord(c.Request().Context(), h.auditFrom(c, "role.delete", "role", id, rl.Name))
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- RoleBinding ----

type createBindingReq struct {
	UserID     int    `json:"user_id"`
	RoleID     int    `json:"role_id"`
	ScopeLevel string `json:"scope_level"`      // org | team
	TeamID     string `json:"team_id"`          // team scope 时必填
	ExpiresIn  *int   `json:"expires_in_hours"` // 可选，临时授权小时数
}

// listBindings 角色绑定列表。
//
// @Summary      角色绑定列表
// @Tags         rbac
// @Produce      json
// @Success      200  {array}   ent.RoleBinding
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /role-bindings [get]
func (h *Handler) listBindings(c echo.Context) error {
	bs, err := h.db.RoleBinding.Query().WithRole().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, bs)
}

// createBinding 创建角色绑定（把 Role 授予 User）。
//
// @Summary      创建角色绑定
// @Tags         rbac
// @Accept       json
// @Produce      json
// @Param        body  body     createBindingReq  true  "绑定参数"
// @Success      201   {object} ent.RoleBinding
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /role-bindings [post]
func (h *Handler) createBinding(c echo.Context) error {
	var req createBindingReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.UserID == 0 || req.RoleID == 0 {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "user_id and role_id required"})
	}
	scope := rolebinding.ScopeLevel(req.ScopeLevel)
	if scope != rolebinding.ScopeLevelOrg && scope != rolebinding.ScopeLevelTeam {
		scope = rolebinding.ScopeLevelTeam
	}
	if scope == rolebinding.ScopeLevelTeam && req.TeamID == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "team_id required for team scope"})
	}
	b := h.db.RoleBinding.Create().
		SetUserID(req.UserID).
		SetRoleID(req.RoleID).
		SetScopeLevel(scope).
		SetGrantedAt(time.Now())
	if req.TeamID != "" {
		b.SetTeamID(req.TeamID)
	}
	if req.ExpiresIn != nil && *req.ExpiresIn > 0 {
		b.SetExpiresAt(time.Now().Add(time.Duration(*req.ExpiresIn) * time.Hour))
	}
	saved, err := b.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	if h.audit != nil {
		e := h.auditFrom(c, "role.assign", "role_binding", saved.ID, "")
		e.Detail = map[string]any{"user_id": req.UserID, "role_id": req.RoleID, "scope": scope}
		h.audit.MustRecord(c.Request().Context(), e)
	}
	return c.JSON(http.StatusCreated, saved)
}

// deleteBinding 删除角色绑定（撤销授权）。
//
// @Summary      删除角色绑定
// @Tags         rbac
// @Param        id   path  int  true  "绑定 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /role-bindings/{id} [delete]
func (h *Handler) deleteBinding(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.RoleBinding.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	if h.audit != nil {
		h.audit.MustRecord(c.Request().Context(), h.auditFrom(c, "role.unassign", "role_binding", id, ""))
	}
	return c.NoContent(http.StatusNoContent)
}
