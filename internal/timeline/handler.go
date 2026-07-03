// handler.go 时间线查询与追加 API（能力域 10）。
package timeline

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行写操作（追加时间线），造成"报 403 却已落库"的越权。故 checkAccess
// 拒绝时返回本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler 时间线 API。
type Handler struct {
	recorder *Recorder
	authz    *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope    *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建时间线 handler。
func NewHandler(r *Recorder) *Handler {
	return &Handler{recorder: r}
}

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// actorFromContext 取当前操作人 ID（鉴权中间件注入的 ctxUser）。
// 中间件未注入（匿名放行）时返回 0。
func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 incident 是否有 perm 权限。
// 时间线按 incident id 查询/追加，资源 kind 固定为 "incident"。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, "incident", id)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, incID, auth.PermIncidentView); e != nil {
		return e
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	typeFilter := timelineitem.Type(c.QueryParam("type"))
	sourceFilter := timelineitem.Source(c.QueryParam("source"))

	items, err := h.recorder.Query(c.Request().Context(), incID, typeFilter, sourceFilter, limit, offset)
	if err != nil {
		return errs.Internal(c, nil, err)
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
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, incID, auth.PermIncidentView); e != nil {
		return e
	}
	var req addReq
	if err := c.Bind(&req); err != nil || req.Content == "" {
		return errs.BadRequest(c, "content required")
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
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "recorded"})
}
