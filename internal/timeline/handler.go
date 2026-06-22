// handler.go 时间线查询与追加 API（能力域 10）。
package timeline

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 时间线 API。
type Handler struct {
	recorder *Recorder
}

// NewHandler 创建时间线 handler。
func NewHandler(r *Recorder) *Handler {
	return &Handler{recorder: r}
}

// Register 挂载路由。
// GET  /incidents/:id/timeline          查询时间线（?type=&source=&limit=&offset=）
// POST /incidents/:id/timeline          手动追加条目（备注）
func (h *Handler) Register(g *echo.Group) {
	g.GET("/incidents/:id/timeline", h.list)
	g.POST("/incidents/:id/timeline", h.add)
}

// ListTimeline 查询某事件的时间线。
//
// @Summary      List timeline items
// @Description  按 type/source/limit/offset 过滤返回事件时间线条目（分页）。
// @Tags         timeline
// @Produce      json
// @Param        id      path   int     true   "Incident ID"
// @Param        type    query  string  false  "条目类型过滤"
// @Param        source  query  string  false  "来源过滤"
// @Param        limit   query  int     false  "返回条数"
// @Param        offset  query  int     false  "偏移量"
// @Success      200  {object}  httputil.Paginated[ent.TimelineItem]
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/timeline [get]
// @Security     bearerAuth
func (h *Handler) list(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	typeFilter := timelineitem.Type(c.QueryParam("type"))
	sourceFilter := timelineitem.Source(c.QueryParam("source"))

	items, err := h.recorder.Query(c.Request().Context(), incID, typeFilter, sourceFilter, limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	total, _ := h.recorder.Count(c.Request().Context(), incID)
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.TimelineItem]{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// addReq 手动追加条目请求（响应者备注等）。
type addReq struct {
	Content string         `json:"content"`
	Actor   Actor          `json:"actor"`
	Source  string         `json:"source"` // web | im | api
	Detail  map[string]any `json:"detail"`
}

// AddTimeline 手动追加时间线条目。
//
// @Summary      Add timeline item
// @Description  手动追加一条 note_added 时间线条目（响应者备注等）。
// @Tags         timeline
// @Accept       json
// @Produce      json
// @Param        id       path      int      true  "Incident ID"
// @Param        request  body      addReq   true  "条目内容（content 必填）"
// @Success      201      {object}  map[string]string  "{status: recorded}"
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/timeline [post]
// @Security     bearerAuth
func (h *Handler) add(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	var req addReq
	if err := c.Bind(&req); err != nil || req.Content == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "content required"})
	}
	// 默认 note_added 类型、web 来源
	actor := req.Actor
	if actor.Kind == "" {
		actor.Kind = "user"
	}
	src := timelineitem.Source(req.Source)
	if src == "" {
		src = timelineitem.SourceWeb
	}
	if err := h.recorder.Record(c.Request().Context(), incID,
		timelineitem.TypeNoteAdded, req.Content, actor, src, req.Detail); err != nil {
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "recorded"})
}
