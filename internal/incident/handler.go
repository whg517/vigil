// handler.go Incident API（能力域 14 集成 + 8 IM/Web 操作入口）。
//
// 暴露 incident 查询与操作，是 IM 卡片/Web/外部系统的统一入口。
// 复用 incident.Service（含 ack/resolve/escalate + 时间线记录 + 升级取消）。
package incident

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v4"
)

// Handler Incident API。
type Handler struct {
	db  *ent.Client
	svc *Service
}

// NewHandler 创建 incident handler。
func NewHandler(db *ent.Client, svc *Service) *Handler {
	return &Handler{db: db, svc: svc}
}

// Register 挂载路由。
// GET    /incidents           列表（?status=&severity=&limit=&offset=）
// GET    /incidents/:id       详情
// POST   /incidents/:id/ack   确认
// POST   /incidents/:id/resolve 解决
// POST   /incidents/:id/escalate 升级
func (h *Handler) Register(g *echo.Group) {
	g.GET("/incidents", h.list)
	g.GET("/incidents/:id", h.get)
	g.POST("/incidents/:id/ack", h.ack)
	g.POST("/incidents/:id/resolve", h.resolve)
	g.POST("/incidents/:id/escalate", h.escalate)
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
func (h *Handler) list(c echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Incident.Query()
	if s := c.QueryParam("status"); s != "" {
		q = q.Where(incident.StatusEQ(incident.Status(s)))
	}
	if s := c.QueryParam("severity"); s != "" {
		q = q.Where(incident.SeverityEQ(incident.Severity(s)))
	}
	// 在加 limit/offset 前 clone 出计数 query，保证 total 与列表筛选条件一致
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
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
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
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
func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	inc, err := h.db.Incident.Query().
		Where(incident.IDEQ(id)).
		WithResponders().
		WithEvents().
		Only(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "not found"})
	}
	return c.JSON(http.StatusOK, inc)
}

// actorFromContext 取当前操作人 ID。
// 来自鉴权中间件注入的 ctxUser（auth.UserIDFromContext）。
// 渐进式鉴权阶段：中间件可能未注入（匿名放行），此时返回 0（视为系统/匿名操作）。
// 对应 CLAUDE.md 边界：不绕过 RBAC，actor 不再来自请求 body（防伪造）。
func (h *Handler) actorFromContext(c echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// ack 确认事件（@Router /incidents/{id}/ack）。
//
// @Summary      确认事件（ack）
// @Tags         incident
// @Produce      json
// @Param        id   path     int  true  "事件 ID"
// @Success      200  {object} ent.Incident
// @Failure      400  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /incidents/{id}/ack [post]
func (h *Handler) ack(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	inc, err := h.svc.Ack(c.Request().Context(), id, h.actorFromContext(c), SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
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
// @Security     bearerAuth
// @Router       /incidents/{id}/resolve [post]
func (h *Handler) resolve(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	inc, err := h.svc.Resolve(c.Request().Context(), id, h.actorFromContext(c), SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
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
// @Security     bearerAuth
// @Router       /incidents/{id}/escalate [post]
func (h *Handler) escalate(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	inc, err := h.svc.Escalate(c.Request().Context(), id, h.actorFromContext(c), SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, inc)
}
