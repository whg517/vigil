// handler.go AI 诊断 API（能力域 11）。
package ai

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"

	"github.com/labstack/echo/v5"
)

// errAccessDenied 哨兵错误：checkAccess 已写出 403/500 响应，handler 应立即 return 中止后续逻辑。
//
// 背景：errs.Forbidden/Internal 写完响应后按 echo 惯例返回 nil，若 checkAccess 直接把该 nil
// 透传给调用方，则 `if e := checkAccess(...); e != nil { return e }` 永不触发，handler 会在
// 已写 403 的情况下继续执行后续逻辑（触发 AI 诊断），造成"报 403 却已执行"的越权。故
// checkAccess 拒绝时返回本哨兵（非 nil），调用方据此中止；响应已提交，echo 错误处理器会跳过二次写。
var errAccessDenied = errors.New("access denied (response already written)")

// Handler AI 诊断 API。
type Handler struct {
	engine *DiagnoseEngine
	authz  *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
}

// NewHandler 创建 AI handler。
func NewHandler(e *DiagnoseEngine) *Handler {
	return &Handler{engine: e}
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

// checkAccess 资源级鉴权 helper（SEC-01）：校验当前用户对 (kind,id) 资源是否有 perm 权限。
// 返回 echo error 形式，handler 直接 return。authz/scope 为 nil 时放行（兼容渐进/单测）。
func (h *Handler) checkAccess(c *echo.Context, kind string, id int, perm auth.Permission) error {
	if h.authz == nil || h.scope == nil {
		return nil // 未注入：降级放行（渐进/单测）
	}
	allowed, err := auth.CheckResourceAccess(c.Request().Context(), h.authz, h.scope, h.actorFromContext(c), perm, kind, id)
	if err != nil {
		// errs.Internal 写完 500 返回 nil，必须换成非 nil 哨兵，否则调用方不会中止。
		_ = errs.Internal(c, nil, err)
		return errAccessDenied
	}
	if !allowed {
		// 同理：errs.Forbidden 写完 403 返回 nil，返回哨兵让调用方 return 中止后续逻辑。
		_ = errs.Forbidden(c, "")
		return errAccessDenied
	}
	return nil
}

// Register 挂载路由。
// POST /incidents/:id/diagnose     触发 AI 根因诊断
// GET  /incidents/:id/similar      查询相似历史事件
// GET  /incidents/:id/similar-postmortems  查询相似已发布复盘（知识沉淀 M12.6）
// POST /ai-insights/:id/resolve    人确认/拒绝 AI 建议（human-in-the-loop）
func (h *Handler) Register(g *echo.Group) {
	g.POST("/incidents/:id/diagnose", h.diagnose)
	g.GET("/incidents/:id/similar", h.similar)
	g.GET("/incidents/:id/similar-postmortems", h.similarPostmortems)
	g.POST("/ai-insights/:id/resolve", h.resolve)
}

// DiagnoseIncident 触发根因诊断。
//
// @Summary      Diagnose incident (AI)
// @Description  触发 LLM 根因诊断；若未启用 LLM 返回 200 与 disabled 状态。
// @Tags         ai
// @Produce      json
// @Param        id   path      int  true  "Incident ID"
// @Success      200  {object}  map[string]string  "AI 诊断未启用"
// @Success      201  {object}  ai.DiagnoseResult
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/diagnose [post]
// @Security     bearerAuth
func (h *Handler) diagnose(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "incident", incID, auth.PermIncidentView); e != nil {
		return e
	}
	res, err := h.engine.Diagnose(c.Request().Context(), incID)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	if res == nil {
		// res==nil：LLM 未配置 或 调用失败降级（FIX-C，见 diagnose.go）。
		// 两种情况都走 disabled，让前端用规则兜底；失败原因在后端日志，不泄露前端。
		return c.JSON(http.StatusOK, map[string]string{"status": "disabled", "message": "AI 诊断暂不可用（未配置或调用失败，已降级）"})
	}
	return c.JSON(http.StatusCreated, res)
}

// FindSimilarIncidents 查询相似历史事件。
//
// @Summary      Find similar incidents
// @Description  按向量/文本相似度查询与给定 incident 相似的历史事件。
// @Tags         ai
// @Produce      json
// @Param        id     path   int  true   "Incident ID"
// @Param        limit  query  int  false  "返回条数上限"
// @Success      200    {object}  map[string]any  "{similar: []*ent.Incident}"
// @Failure      400    {object}  httputil.ErrorResponse
// @Failure      500    {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/similar [get]
// @Security     bearerAuth
func (h *Handler) similar(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "incident", incID, auth.PermIncidentView); e != nil {
		return e
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	similar, err := h.engine.FindSimilar(c.Request().Context(), incID, limit)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"similar": similar})
}

// similarPostmortems 查询相似的已发布复盘（知识沉淀 M12.6）。
// "上次类似故障是怎么处理的"——published 复盘反哺新事件诊断。
//
// @Summary      Find similar postmortems
// @Description  按相似度查询与给定 incident 相似的已发布复盘（知识反哺诊断）。
// @Tags         ai
// @Produce      json
// @Param        id     path   int  true   "Incident ID"
// @Param        limit  query  int  false  "返回条数上限"
// @Success      200    {object}  map[string]any  "{similar_postmortems: []*ent.Postmortem}"
// @Failure      400    {object}  httputil.ErrorResponse
// @Failure      500    {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/similar-postmortems [get]
// @Security     bearerAuth
func (h *Handler) similarPostmortems(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "incident", incID, auth.PermIncidentView); e != nil {
		return e
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	pms, err := h.engine.FindSimilarPostmortems(c.Request().Context(), incID, limit)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"similar_postmortems": pms})
}

// resolveReq 确认/拒绝请求。
type resolveReq struct {
	Accepted bool `json:"accepted"`
}

// ResolveAIInsight 人确认/拒绝 AI 建议。
//
// @Summary      Resolve AI insight
// @Description  人确认/拒绝 AI 建议（human-in-the-loop）。
// @Tags         ai
// @Accept       json
// @Produce      json
// @Param        id       path      int          true  "AI Insight ID"
// @Param        request  body      resolveReq   true  "accepted=true 接受，false 拒绝"
// @Success      200      {object}  map[string]any
// @Failure      400      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /ai-insights/{id}/resolve [post]
// @Security     bearerAuth
func (h *Handler) resolve(c *echo.Context) error {
	insightID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "ai_insight", insightID, auth.PermIncidentView); e != nil {
		return e
	}
	var req resolveReq
	_ = c.Bind(&req)
	if err := h.engine.ResolveInsight(c.Request().Context(), insightID, req.Accepted); err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "resolved", "accepted": req.Accepted})
}
