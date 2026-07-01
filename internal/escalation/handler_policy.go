// handler_policy.go 升级策略管理 API（能力域 6 升级，PRD M6.x）。
//
// 此前 escalation 包只有 engine（被 triage 在内存调用），无 CRUD handler。
// 本文件补 list/get/create/update/delete，供前端管理升级层级配置。
//
// 注意：与 engine.go 的 HandleTask（Asynq 任务处理）区分，本文件是 HTTP CRUD。
package escalation

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// PolicyHandler 升级策略管理 API（与处理升级任务的 Engine 区分）。
type PolicyHandler struct {
	db *ent.Client
}

// NewPolicyHandler 创建升级策略 handler。
func NewPolicyHandler(db *ent.Client) *PolicyHandler {
	return &PolicyHandler{db: db}
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
	policies, err := h.db.EscalationPolicy.Query().All(c.Request().Context())
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
	b := h.db.EscalationPolicy.Create().SetName(req.Name).SetRepeatTimes(req.RepeatTimes)
	if len(req.Levels) > 0 {
		b.SetLevels(req.Levels)
	}
	policy, err := b.Save(c.Request().Context())
	if err != nil {
		return errs.Internal(c, nil, err)
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
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
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
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
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
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.EscalationPolicy.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}
