// handler.go 排班 API handler。
package schedule

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	entschedule "github.com/kevin/vigil/ent/schedule"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 排班 API。
type Handler struct {
	engine *Engine
	db     *ent.Client
}

// NewHandler 创建排班 handler。db 非 nil 时启用 Schedule CRUD（前端枚举/管理用）。
func NewHandler(e *Engine, db *ent.Client) *Handler {
	return &Handler{engine: e, db: db}
}

// Register 把排班路由挂到 group。
// CRUD（db 非 nil 时启用）+ 查询。
func (h *Handler) Register(g *echo.Group) {
	// CRUD（能力域 5，权限点 schedule.* 由调用方在装配时授权）
	if h.db != nil {
		g.GET("/schedules", h.list)
		g.POST("/schedules", h.create)
		g.GET("/schedules/:id", h.get)
		g.PATCH("/schedules/:id", h.update)
		g.DELETE("/schedules/:id", h.delete)
	}
	// 查询：某时刻在班人 + 预览
	g.GET("/schedules/:id/oncall", h.oncall)
	g.GET("/schedules/:id/preview", h.preview)
}

// ===== Schedule CRUD =====

// list 排班列表。
//
// @Summary      排班列表
// @Tags         schedule
// @Produce      json
// @Success      200  {array}   ent.Schedule
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules [get]
func (h *Handler) list(c *echo.Context) error {
	schedules, err := h.db.Schedule.Query().All(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, schedules)
}

// createScheduleReq 创建排班请求。
type createScheduleReq struct {
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`     // calendar | rotation | follow_the_sun
	Timezone string                 `json:"timezone"` // 默认 Asia/Shanghai
	Layers   []schema.ScheduleLayer `json:"layers"`
	TeamID   int                    `json:"team_id"`
}

// create 创建排班。
//
// @Summary      创建排班
// @Tags         schedule
// @Accept       json
// @Produce      json
// @Param        body  body     createScheduleReq  true  "排班创建参数"
// @Success      201   {object} ent.Schedule
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules [post]
func (h *Handler) create(c *echo.Context) error {
	var req createScheduleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "name required"})
	}
	typ := "rotation"
	if req.Type != "" {
		typ = req.Type
	}
	tz := req.Timezone
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	b := h.db.Schedule.Create().
		SetName(req.Name).
		SetType(entschedule.Type(typ)).
		SetTimezone(tz)
	if len(req.Layers) > 0 {
		b.SetLayers(req.Layers)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	s, err := b.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusCreated, s)
}

// get 排班详情。
//
// @Summary      排班详情
// @Tags         schedule
// @Produce      json
// @Param        id   path     int  true  "排班 ID"
// @Success      200  {object} ent.Schedule
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id} [get]
func (h *Handler) get(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	s, err := h.db.Schedule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, s)
}

// updateScheduleReq 更新排班请求（全部字段可选）。
type updateScheduleReq struct {
	Name     *string                 `json:"name"`
	Type     *string                 `json:"type"`
	Timezone *string                 `json:"timezone"`
	Layers   *[]schema.ScheduleLayer `json:"layers"`
}

// update 更新排班。
//
// @Summary      更新排班
// @Tags         schedule
// @Accept       json
// @Produce      json
// @Param        id    path     int                true  "排班 ID"
// @Param        body  body     updateScheduleReq  true  "排班更新参数"
// @Success      200   {object} ent.Schedule
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id} [patch]
func (h *Handler) update(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req updateScheduleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	upd := h.db.Schedule.UpdateOneID(id)
	if req.Name != nil {
		upd.SetName(*req.Name)
	}
	if req.Type != nil {
		upd.SetType(entschedule.Type(*req.Type))
	}
	if req.Timezone != nil {
		upd.SetTimezone(*req.Timezone)
	}
	if req.Layers != nil {
		upd.SetLayers(*req.Layers)
	}
	s, err := upd.Save(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, s)
}

// delete 删除排班。
//
// @Summary      删除排班
// @Tags         schedule
// @Param        id   path  int  true  "排班 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id} [delete]
func (h *Handler) delete(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	if err := h.db.Schedule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

// oncall 查询某 Schedule 在指定时刻的在班人。
// query: time（ISO8601，可选，默认 now）
//
// @Summary      查询在班人
// @Description  按时刻（RFC3339，默认 now）计算某排班的在班人，按层优先级排序。
// @Tags         schedule
// @Produce      json
// @Param        id    path     int     true  "排班 ID"
// @Param        time  query    string  false  "查询时刻（RFC3339，默认 now）"
// @Success      200   {object} schedule.OncallResult
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      404   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id}/oncall [get]
func (h *Handler) oncall(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}

	var at time.Time
	if t := c.QueryParam("time"); t != "" {
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid time (use RFC3339)"})
		}
		at = parsed
	}

	res, err := h.engine.Oncall(c.Request().Context(), id, at)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "schedule not found"})
		}
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// preview 预览未来 N 天排班。
// query: days（默认 14）
//
// @Summary      预览排班
// @Description  预览未来 N 天（默认 14，上限 90）的每日在班人。
// @Tags         schedule
// @Produce      json
// @Param        id    path     int  true  "排班 ID"
// @Param        days  query    int  false "预览天数（默认 14，上限 90）"  default(14) maximum(90)
// @Success      200   {object} schedule.PreviewResult
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      404   {object} httputil.ErrorResponse
// @Failure      500   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id}/preview [get]
func (h *Handler) preview(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	days := 14
	if d := c.QueryParam("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 90 {
			days = parsed
		}
	}

	res, err := h.engine.Preview(c.Request().Context(), id, days)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "schedule not found"})
		}
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, PreviewResult{ScheduleID: id, Days: res})
}
