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

	"github.com/labstack/echo/v4"
)

// Handler Incident API。
type Handler struct {
	db *ent.Client
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

// list 查询事件列表。
func (h *Handler) list(c echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.Incident.Query()
	if s := c.QueryParam("status"); s != "" {
		q = q.Where(incident.StatusEQ(incident.Status(s)))
	}
	if s := c.QueryParam("severity"); s != "" {
		q = q.Where(incident.SeverityEQ(incident.Severity(s)))
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	total, _ := h.db.Incident.Query().Count(ctx)
	return c.JSON(http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

// get 事件详情（含 responders/events）。
func (h *Handler) get(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	inc, err := h.db.Incident.Query().
		Where(incident.IDEQ(id)).
		WithResponders().
		WithEvents().
		Only(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, inc)
}

// actionReq 操作请求（actor 从鉴权 context 取，这里简化用 body）。
type actionReq struct {
	ActorID int `json:"actor_id"` // 操作人（后续从鉴权 context 取）
}

func (h *Handler) ack(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req actionReq
	_ = c.Bind(&req)
	inc, err := h.svc.Ack(c.Request().Context(), id, req.ActorID, SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, inc)
}

func (h *Handler) resolve(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req actionReq
	_ = c.Bind(&req)
	inc, err := h.svc.Resolve(c.Request().Context(), id, req.ActorID, SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, inc)
}

func (h *Handler) escalate(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req actionReq
	_ = c.Bind(&req)
	inc, err := h.svc.Escalate(c.Request().Context(), id, req.ActorID, SourceWeb)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, inc)
}
