// handler.go 排班 API handler。
package schedule

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	entoverride "github.com/kevin/vigil/ent/override"
	entrotation "github.com/kevin/vigil/ent/rotation"
	entschedule "github.com/kevin/vigil/ent/schedule"
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

// Handler 排班 API。
type Handler struct {
	engine *Engine
	db     *ent.Client
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建排班 handler。db 非 nil 时启用 Schedule CRUD（前端枚举/管理用）。
func NewHandler(e *Engine, db *ent.Client) *Handler {
	return &Handler{engine: e, db: db}
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

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 schedule 是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "schedule", id)
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
	// Override 换班（C5/M5.3）：创建/查询/删除。db 非 nil 时启用。
	if h.db != nil {
		g.POST("/schedules/:id/overrides", h.createOverride)
		g.GET("/schedules/:id/overrides", h.listOverrides)
		g.DELETE("/schedules/:id/overrides/:oid", h.deleteOverride)
	}
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
	ctx := c.Request().Context()
	q := h.db.Schedule.Query()
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
					return c.JSON(http.StatusOK, []*ent.Schedule{})
				}
				q = q.Where(entschedule.HasTeamWith(team.IDIn(teamIDs...)))
			}
		}
	}
	schedules, err := q.All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, schedules)
}

// createScheduleReq 创建排班请求。
type createScheduleReq struct {
	Name     string           `json:"name"`
	Type     string           `json:"type"`     // calendar | rotation | follow_the_sun
	Timezone string           `json:"timezone"` // 默认 Asia/Shanghai
	Layers   []createLayerReq `json:"layers"`
	TeamID   int              `json:"team_id"`
}

// createLayerReq 创建排班分层请求（FIX-D：含 rotation 配置，建 Rotation 实体）。
// 修复前 layers 只存 JSON 不建 Rotation → oncall 查不到 rotation → 返回空。
type createLayerReq struct {
	Name         string `json:"name"`          // 层名，如 "一线"
	Priority     int    `json:"priority"`      // 数字越小优先级越高
	Participants []int  `json:"participants"`  // 值班人 user id 列表
	RotationType string `json:"rotation_type"` // daily | weekly | custom（默认 daily）
	ShiftLength  string `json:"shift_length"`  // 班次时长 "24h"/"1week"（默认 24h）
	HandoffTime  string `json:"handoff_time"`  // 交接时刻 "HH:MM"（默认 09:00）
	StartDate    string `json:"start_date"`    // 开始日期 RFC3339（默认现在）
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
		return errs.BadRequest(c, "invalid body")
	}
	if req.Name == "" {
		return errs.BadRequest(c, "name required")
	}
	ctx := c.Request().Context()
	typ := "rotation"
	if req.Type != "" {
		typ = req.Type
	}
	tz := req.Timezone
	if tz == "" {
		tz = "Asia/Shanghai"
	}

	// FIX-D：用事务创建 Schedule + 各 layer 对应的 Rotation 实体，保证一致性。
	// 修复前只 SetLayers(JSON)，不建 Rotation → Oncall 查 sched.QueryRotations() 为空。
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	rollback := func() { _ = tx.Rollback() }

	layers := make([]schema.ScheduleLayer, 0, len(req.Layers))
	rotationIDs := make([]int, 0, len(req.Layers))
	for _, lr := range req.Layers {
		// 无 participants 的 layer 跳过 Rotation 创建（无法算在班人，仅保留层信息）
		if len(lr.Participants) == 0 {
			layers = append(layers, schema.ScheduleLayer{Name: lr.Name, Priority: lr.Priority})
			continue
		}
		rot, rerr := buildRotation(ctx, tx, lr)
		if rerr != nil {
			rollback()
			return errs.BadRequest(c, "invalid layer "+lr.Name+": "+rerr.Error())
		}
		rotationIDs = append(rotationIDs, rot.ID)
		layers = append(layers, schema.ScheduleLayer{
			ID:         strconv.Itoa(rot.ID),
			Name:       lr.Name,
			Priority:   lr.Priority,
			RotationID: strconv.Itoa(rot.ID),
		})
	}

	b := tx.Schedule.Create().
		SetName(req.Name).
		SetType(entschedule.Type(typ)).
		SetTimezone(tz)
	if len(layers) > 0 {
		b.SetLayers(layers)
	}
	if req.TeamID > 0 {
		b.SetTeamID(req.TeamID)
	}
	if len(rotationIDs) > 0 {
		b.AddRotationIDs(rotationIDs...)
	}
	s, err := b.Save(ctx)
	if err != nil {
		rollback()
		return errs.FailConstraint(c, nil, err, "schedule", "schedule already exists")
	}
	if err := tx.Commit(); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, s)
}

// buildRotation 从 createLayerReq 构造 Rotation 实体（FIX-D）。
// 解析 participants/rotation_type/shift_length/start_date，用事务 client 持久化。
// 调用方负责把返回的 rotation.ID 关联到 Schedule 并回填 layer.RotationID。
func buildRotation(ctx context.Context, tx *ent.Tx, lr createLayerReq) (*ent.Rotation, error) {
	rt := entrotation.RotationTypeDaily
	switch strings.ToLower(lr.RotationType) {
	case "weekly":
		rt = entrotation.RotationTypeWeekly
	case "custom":
		rt = entrotation.RotationTypeCustom
	case "", "daily":
		// 默认 daily
	default:
		return nil, fmt.Errorf("invalid rotation_type %q", lr.RotationType)
	}
	shiftLen := lr.ShiftLength
	if shiftLen == "" {
		shiftLen = "24h"
	}
	handoff := lr.HandoffTime
	if handoff == "" {
		handoff = "09:00"
	}
	start := time.Now()
	if lr.StartDate != "" {
		parsed, err := time.Parse(time.RFC3339, lr.StartDate)
		if err != nil {
			return nil, fmt.Errorf("invalid start_date: %w", err)
		}
		start = parsed
	}
	rb := tx.Rotation.Create().
		SetName(lr.Name).
		SetRotationType(rt).
		SetShiftLength(shiftLen).
		SetHandoffTime(handoff).
		SetStartDate(start).
		AddParticipantIDs(lr.Participants...)
	return rb.Save(ctx)
}

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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
	}
	s, err := h.db.Schedule.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, s)
}

// updateScheduleReq 更新排班请求（全部字段可选）。
//
// B21：Layers 用与创建一致的 createLayerReq 形状（含 participants/rotation 配置），
// 提供 layers 时重建 Rotation（删旧建新），使"改参与人"可经 PATCH 完成，
// 无需删除整个 Schedule 重建。
type updateScheduleReq struct {
	Name     *string           `json:"name"`
	Type     *string           `json:"type"`
	Timezone *string           `json:"timezone"`
	Layers   *[]createLayerReq `json:"layers"`
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
	}
	var req updateScheduleReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	ctx := c.Request().Context()

	// B21：提供 layers 时重建 Rotation（删旧建新）+ 写 layers JSON，用事务保证一致。
	// 未提供 layers 时只改标量字段（无事务开销）。
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	rollback := func() { _ = tx.Rollback() }

	upd := tx.Schedule.UpdateOneID(id)
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
		sched, gerr := tx.Schedule.Get(ctx, id)
		if gerr != nil {
			rollback()
			if ent.IsNotFound(gerr) {
				return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
			}
			return errs.Internal(c, nil, gerr)
		}
		// 删旧 Rotation（避免残留旧参与人被解算）。
		oldRots, qerr := sched.QueryRotations().All(ctx)
		if qerr != nil {
			rollback()
			return errs.Internal(c, nil, qerr)
		}
		for _, r := range oldRots {
			if derr := tx.Rotation.DeleteOneID(r.ID).Exec(ctx); derr != nil {
				rollback()
				return errs.Internal(c, nil, derr)
			}
		}
		// 建新 Rotation + 组装 layers JSON（与 create 一致）。
		layers := make([]schema.ScheduleLayer, 0, len(*req.Layers))
		newRotIDs := make([]int, 0, len(*req.Layers))
		for _, lr := range *req.Layers {
			if len(lr.Participants) == 0 {
				layers = append(layers, schema.ScheduleLayer{Name: lr.Name, Priority: lr.Priority})
				continue
			}
			rot, rerr := buildRotation(ctx, tx, lr)
			if rerr != nil {
				rollback()
				return errs.BadRequest(c, "invalid layer "+lr.Name+": "+rerr.Error())
			}
			newRotIDs = append(newRotIDs, rot.ID)
			layers = append(layers, schema.ScheduleLayer{
				ID:         strconv.Itoa(rot.ID),
				Name:       lr.Name,
				Priority:   lr.Priority,
				RotationID: strconv.Itoa(rot.ID),
			})
		}
		upd.ClearRotations().SetLayers(layers)
		if len(newRotIDs) > 0 {
			upd.AddRotationIDs(newRotIDs...)
		}
	}

	s, err := upd.Save(ctx)
	if err != nil {
		rollback()
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
	}
	if err := tx.Commit(); err != nil {
		return errs.Internal(c, nil, err)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
	}
	if err := h.db.Schedule.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
		}
		return errs.Internal(c, nil, err)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
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
		return errs.Internal(c, nil, err)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
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
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, PreviewResult{ScheduleID: id, Days: res})
}

// ===== Override 换班（C5/M5.3）=====

// overrideView Override 响应视图（避免直接暴露 ent 关系，含顶替人/创建人展开）。
type overrideView struct {
	ID          int       `json:"id"`
	ScheduleID  int       `json:"schedule_id"`
	UserID      int       `json:"user_id"`    // 顶替人
	UserName    string    `json:"user_name"`  // 顶替人显示名
	CreatedByID int       `json:"created_by"` // 创建人（自我换班=顶替对象，admin 指派=管理员）
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"created_at"`
}

func toOverrideView(o *ent.Override) overrideView {
	v := overrideView{
		ID: o.ID, StartTime: o.StartTime, EndTime: o.EndTime,
		Reason: o.Reason, CreatedAt: o.CreatedAt,
	}
	if sched := o.Edges.Schedule; sched != nil {
		v.ScheduleID = sched.ID
	}
	if u := o.Edges.User; u != nil {
		v.UserID = u.ID
		v.UserName = u.Name
	}
	if cb := o.Edges.CreatedBy; cb != nil {
		v.CreatedByID = cb.ID
	}
	return v
}

// createOverrideReq 创建换班请求。
type createOverrideReq struct {
	UserID    int    `json:"user_id"`    // 顶替人 user id
	StartTime string `json:"start_time"` // RFC3339
	EndTime   string `json:"end_time"`   // RFC3339
	Reason    string `json:"reason"`
}

// createOverride 创建临时换班（C5/M5.3）。
//
// 权限：schedule.override（路由守卫已校验）+ 资源级 scope（本 schedule 所属 team）。
// 换己班（user_id==actor）仅需 schedule.override；换他人（user_id!=actor）需管理级
// 权限（schedule.update，团队管理员/超管具备），防止值班人越权指派他人替班。
//
// @Summary      创建换班 Override
// @Tags         schedule
// @Accept       json
// @Produce      json
// @Param        id    path     int                true  "排班 ID"
// @Param        body  body     createOverrideReq  true  "换班参数"
// @Success      201   {object} schedule.overrideView
// @Failure      400   {object} httputil.ErrorResponse
// @Failure      403   {object} httputil.ErrorResponse
// @Failure      404   {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id}/overrides [post]
func (h *Handler) createOverride(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleOverride); e != nil {
		return e
	}
	ctx := c.Request().Context()
	// schedule 须存在（避免对不存在的 schedule 建 override）。
	if _, err := h.db.Schedule.Get(ctx, id); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "schedule not found"})
		}
		return errs.Internal(c, nil, err)
	}

	var req createOverrideReq
	if err := c.Bind(&req); err != nil {
		return errs.BadRequest(c, "invalid body")
	}
	if req.UserID <= 0 {
		return errs.BadRequest(c, "user_id required")
	}
	start, end, berr := parseOverrideWindow(req.StartTime, req.EndTime)
	if berr != nil {
		return errs.BadRequest(c, berr.Error())
	}

	actor := h.actorFromContext(c)
	// 换他人须管理级权限（oncall 只能换己班）。actor<=0（匿名/单测）时降级放行。
	if actor > 0 && req.UserID != actor {
		if e := h.requireOverrideOthers(c, id); e != nil {
			return e
		}
	}

	b := h.db.Override.Create().
		SetScheduleID(id).
		SetUserID(req.UserID).
		SetStartTime(start).
		SetEndTime(end)
	if req.Reason != "" {
		b.SetReason(req.Reason)
	}
	if actor > 0 {
		b.SetCreatedByID(actor)
	}
	ov, err := b.Save(ctx)
	if err != nil {
		// 顶替人不存在 / FK 冲突 → 400（客户端可纠正），非 500。
		return errs.FailConstraint(c, nil, err, "override", "invalid user_id or schedule")
	}
	// 回读含关系，返回视图。
	full, err := h.db.Override.Query().
		Where(entoverride.IDEQ(ov.ID)).
		WithSchedule().WithUser().WithCreatedBy().
		Only(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, toOverrideView(full))
}

// requireOverrideOthers 校验 actor 是否有"换他人"的管理级权限（schedule.update，
// 团队管理员/超管具备，oncall 不具备）。以本 schedule 所属 team 为 scope。
// 无权限时已写出 403 响应，返回 errAccessDenied 让调用方中止。
func (h *Handler) requireOverrideOthers(c *echo.Context, schedID int) error {
	// 未注入鉴权器：降级放行（渐进/单测），与 checkAccess 一致。
	if h.authz == nil || h.scope == nil {
		return nil
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), auth.PermScheduleUpdate, "schedule", schedID)
	if err != nil {
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		_ = errs.Forbidden(c, "assigning override for others requires schedule.update")
		return errAccessDenied
	}
	return nil
}

// parseOverrideWindow 解析并校验换班时段（start<end，均 RFC3339 必填）。
func parseOverrideWindow(startStr, endStr string) (time.Time, time.Time, error) {
	if startStr == "" || endStr == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("start_time and end_time required (RFC3339)")
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start_time (use RFC3339)")
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end_time (use RFC3339)")
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end_time must be after start_time")
	}
	return start, end, nil
}

// listOverrides 列出某 Schedule 的换班（C5）。按开始时刻倒序。
//
// @Summary      查询换班列表
// @Tags         schedule
// @Produce      json
// @Param        id   path     int  true  "排班 ID"
// @Success      200  {array}  schedule.overrideView
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id}/overrides [get]
func (h *Handler) listOverrides(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleView); e != nil {
		return e
	}
	ctx := c.Request().Context()
	ovs, err := h.db.Override.Query().
		Where(entoverride.HasScheduleWith(entschedule.IDEQ(id))).
		WithSchedule().WithUser().WithCreatedBy().
		Order(ent.Desc(entoverride.FieldStartTime)).
		All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	views := make([]overrideView, 0, len(ovs))
	for _, o := range ovs {
		views = append(views, toOverrideView(o))
	}
	return c.JSON(http.StatusOK, views)
}

// deleteOverride 删除换班（C5）。权限：schedule.override（路由守卫）+ 资源级 scope。
//
// @Summary      删除换班
// @Tags         schedule
// @Param        id   path  int  true  "排班 ID"
// @Param        oid  path  int  true  "换班 ID"
// @Success      204
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /schedules/{id}/overrides/{oid} [delete]
func (h *Handler) deleteOverride(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	oid, err := strconv.Atoi(c.Param("oid"))
	if err != nil {
		return errs.BadRequest(c, "invalid oid")
	}
	if e := h.checkAccess(c, id, auth.PermScheduleOverride); e != nil {
		return e
	}
	ctx := c.Request().Context()
	// 确认 override 属于该 schedule（避免跨 schedule 删除）。
	exists, err := h.db.Override.Query().
		Where(entoverride.IDEQ(oid), entoverride.HasScheduleWith(entschedule.IDEQ(id))).
		Exist(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if !exists {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "override not found"})
	}
	if err := h.db.Override.DeleteOneID(oid).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "override not found"})
		}
		return errs.Internal(c, nil, err)
	}
	return c.NoContent(http.StatusNoContent)
}
