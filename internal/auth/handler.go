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

	"github.com/labstack/echo/v4"
)

// Handler RBAC 管理 API。
type Handler struct {
	db *ent.Client
}

// NewHandler 创建 RBAC handler。
func NewHandler(db *ent.Client) *Handler {
	return &Handler{db: db}
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

func (h *Handler) listRoles(c echo.Context) error {
	rls, err := h.db.Role.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, rls)
}

func (h *Handler) createRole(c echo.Context) error {
	var req createRoleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
	}
	// 校验权限点合法性（角色配置时必须从系统枚举选）
	for _, p := range req.Permissions {
		if !Permission(p).IsValid() {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid permission: " + p})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, rl)
}

func (h *Handler) deleteRole(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	// 内置角色不可删（builtin=true）
	rl, err := h.db.Role.Get(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "role not found"})
	}
	if rl.Builtin {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "builtin role cannot be deleted"})
	}
	if err := h.db.Role.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- RoleBinding ----

type createBindingReq struct {
	UserID    int    `json:"user_id"`
	RoleID    int    `json:"role_id"`
	ScopeLevel string `json:"scope_level"` // org | team
	TeamID    string `json:"team_id"`       // team scope 时必填
	ExpiresIn *int   `json:"expires_in_hours"` // 可选，临时授权小时数
}

func (h *Handler) listBindings(c echo.Context) error {
	bs, err := h.db.RoleBinding.Query().WithRole().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, bs)
}

func (h *Handler) createBinding(c echo.Context) error {
	var req createBindingReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if req.UserID == 0 || req.RoleID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "user_id and role_id required"})
	}
	scope := rolebinding.ScopeLevel(req.ScopeLevel)
	if scope != rolebinding.ScopeLevelOrg && scope != rolebinding.ScopeLevelTeam {
		scope = rolebinding.ScopeLevelTeam
	}
	if scope == rolebinding.ScopeLevelTeam && req.TeamID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "team_id required for team scope"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, saved)
}

func (h *Handler) deleteBinding(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.db.RoleBinding.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
