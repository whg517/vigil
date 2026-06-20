// handler.go 时间线查询与追加 API（能力域 10）。
package timeline

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent/timelineitem"

	"github.com/labstack/echo/v4"
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

// list 查询某事件的时间线。
func (h *Handler) list(c echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	typeFilter := timelineitem.Type(c.QueryParam("type"))
	sourceFilter := timelineitem.Source(c.QueryParam("source"))

	items, err := h.recorder.Query(c.Request().Context(), incID, typeFilter, sourceFilter, limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	total, _ := h.recorder.Count(c.Request().Context(), incID)
	return c.JSON(http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// addReq 手动追加条目请求（响应者备注等）。
type addReq struct {
	Content string         `json:"content"`
	Actor   Actor          `json:"actor"`
	Source  string         `json:"source"` // web | im | api
	Detail  map[string]any `json:"detail"`
}

// add 手动追加时间线条目。
func (h *Handler) add(c echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var req addReq
	if err := c.Bind(&req); err != nil || req.Content == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "content required"})
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
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "recorded"})
}
