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
	engine   *DiagnoseEngine
	triageAI *TriageAIEngine     // 分诊 AI 引擎（T3.2，可选注入）；nil 时手动触发端点降级返回 disabled
	authz    *auth.Authorizer    // 资源级鉴权（SEC-01，可选注入）
	scope    *auth.ScopeResolver // 资源→team 反查（SEC-01，可选注入）
	audit    *auth.AuditRecorder // 建议改判留痕（S11，可选注入，nil 时跳过）
}

// NewHandler 创建 AI handler。
func NewHandler(e *DiagnoseEngine) *Handler {
	return &Handler{engine: e}
}

// SetTriageAI 注入分诊 AI 引擎（T3.2）：启用 POST /incidents/:id/triage-ai 手动触发端点。
// 为 nil 时该端点降级返回 disabled（与未配置 LLM 一致）。
func (h *Handler) SetTriageAI(t *TriageAIEngine) { h.triageAI = t }

// SetAuthorizer 注入鉴权器（ARCH-02/SEC-01：资源级鉴权）。
// 为 nil 时降级为无资源级校验（兼容渐进启用与单测）。
func (h *Handler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer 使用）。
func (h *Handler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（S11：AI 建议采纳/拒绝留痕）。
func (h *Handler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

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
// POST /incidents/:id/triage-ai    触发分诊 AI（severity/dedup 建议，T3.2）
// GET  /incidents/:id/similar      查询相似历史事件
// GET  /incidents/:id/similar-postmortems  查询相似已发布复盘（知识沉淀 M12.6）
// GET  /incidents/:id/insights     列出该 incident 的历史 AI 洞察（T3.1 可读持久化）
// GET  /ai-insights/:id            取单条 AI 洞察（T3.1）
// POST /ai-insights/:id/resolve    人确认/拒绝 AI 建议（human-in-the-loop）
func (h *Handler) Register(g *echo.Group) {
	g.POST("/incidents/:id/diagnose", h.diagnose)
	g.POST("/incidents/:id/triage-ai", h.triageAIAnalyze)
	g.GET("/incidents/:id/similar", h.similar)
	g.GET("/incidents/:id/similar-postmortems", h.similarPostmortems)
	g.GET("/incidents/:id/insights", h.listInsights)
	g.GET("/ai-insights/:id", h.getInsight)
	g.POST("/ai-insights/:id/resolve", h.resolve)
}

// TriageAIAnalyze 手动触发分诊 AI（T3.2）：产出 severity_adjustment / dedup_suggestion 建议。
//
// 与建单后的异步触发共用引擎；此端点供响应者在 Web/IM 主动「让 AI 再看一眼分诊」。
// 未配置 LLM / 引擎未注入时返回 200 disabled（与 diagnose 一致，让前端走无 AI 兜底）。
// 产出为 human-in-the-loop 建议（status=suggested），返回时告知各类建议是否产出。
//
// @Summary      Trigger triage AI
// @Description  触发分诊 AI，产出严重度调整 / 合并建议（human-in-the-loop）；未启用 LLM 返回 200 disabled。
// @Tags         ai
// @Produce      json
// @Param        id   path      int  true  "Incident ID"
// @Success      200  {object}  map[string]any
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/triage-ai [post]
// @Security     bearerAuth
func (h *Handler) triageAIAnalyze(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "incident", incID, auth.PermIncidentView); e != nil {
		return e
	}
	if h.triageAI == nil || !h.triageAI.available() {
		// 未注入引擎 或 LLM 不可用：降级 disabled（与 diagnose 一致，不报错、不阻断）。
		return c.JSON(http.StatusOK, map[string]string{"status": "disabled", "message": "分诊 AI 暂不可用（未配置或已降级）"})
	}
	res, err := h.triageAI.AnalyzeIncident(c.Request().Context(), incID)
	if err != nil {
		// B25 归一：不存在的 incident → 404 not_found（而非 500）。
		return errs.FailNotFound(c, nil, err, "incident")
	}
	// 返回各类建议是否产出（含产出的 insight_id，前端可直接拉取）。
	out := map[string]any{"status": "analyzed"}
	if res.Severity != nil {
		out["severity_insight_id"] = res.Severity.ID
	}
	if res.Dedup != nil {
		out["dedup_insight_id"] = res.Dedup.ID
	}
	return c.JSON(http.StatusOK, out)
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
// @Failure      404  {object}  httputil.ErrorResponse
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
		// B25 归一：不存在的 incident → 404 not_found（而非 500）；其余内部错误仍走 500。
		return errs.FailNotFound(c, nil, err, "incident")
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
// @Failure      404    {object}  httputil.ErrorResponse
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
		// B25 归一：不存在的 incident → 404 not_found（而非 500）。
		return errs.FailNotFound(c, nil, err, "incident")
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
// @Failure      404    {object}  httputil.ErrorResponse
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
		// B25 归一：不存在的 incident → 404 not_found（而非 500）。
		return errs.FailNotFound(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, map[string]any{"similar_postmortems": pms})
}

// listInsights 列出某 incident 的历史 AI 洞察（T3.1 可读持久化）。
// 诊断产出的 AIInsight 原为 write-only（前端不重新拉取即丢失），补此端点让历史洞察可查、
// accept/reject 状态持久呈现。按创建时间倒序（最新在前）。权限 incident.view。
//
// @Summary      List AI insights of an incident
// @Description  列出该 incident 的全部 AI 洞察（按创建时间倒序），含 status 生命周期。
// @Tags         ai
// @Produce      json
// @Param        id   path      int  true  "Incident ID"
// @Success      200  {object}  map[string]any  "{insights: []*ent.AIInsight}"
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /incidents/{id}/insights [get]
// @Security     bearerAuth
func (h *Handler) listInsights(c *echo.Context) error {
	incID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "incident", incID, auth.PermIncidentView); e != nil {
		return e
	}
	insights, err := h.engine.ListInsights(c.Request().Context(), incID)
	if err != nil {
		// B25 归一：不存在的 incident → 404 not_found（而非 500）。
		return errs.FailNotFound(c, nil, err, "incident")
	}
	return c.JSON(http.StatusOK, map[string]any{"insights": insights})
}

// getInsight 取单条 AI 洞察（T3.1）。权限按 ai_insight→incident→team 反查（与 resolve 一致），
// 但只读用 incident.view（查看即可，无需处置级 ai.insight.resolve）。
//
// @Summary      Get AI insight
// @Description  按 id 取单条 AI 洞察。
// @Tags         ai
// @Produce      json
// @Param        id   path      int  true  "AI Insight ID"
// @Success      200  {object}  ent.AIInsight
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /ai-insights/{id} [get]
// @Security     bearerAuth
func (h *Handler) getInsight(c *echo.Context) error {
	insightID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	if e := h.checkAccess(c, "ai_insight", insightID, auth.PermIncidentView); e != nil {
		return e
	}
	ins, err := h.engine.GetInsight(c.Request().Context(), insightID)
	if err != nil {
		return errs.FailNotFound(c, nil, err, "ai insight")
	}
	return c.JSON(http.StatusOK, ins)
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
// @Failure      404      {object}  httputil.ErrorResponse
// @Failure      409      {object}  httputil.ErrorResponse
// @Failure      500      {object}  httputil.ErrorResponse
// @Router       /ai-insights/{id}/resolve [post]
// @Security     bearerAuth
func (h *Handler) resolve(c *echo.Context) error {
	insightID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}
	// S11：采纳/拒绝是处置级动作，须 ai.insight.resolve（非只读 incident.view）。
	// 否则 subscriber（仅含 incident.view）可越权改判 AI 建议。
	if e := h.checkAccess(c, "ai_insight", insightID, auth.PermAIInsightResolve); e != nil {
		return e
	}
	var req resolveReq
	_ = c.Bind(&req)
	actorID := h.actorFromContext(c)
	ins, err := h.engine.ResolveInsight(c.Request().Context(), insightID, actorID, req.Accepted)
	if err != nil {
		// S11 状态前置校验：已改判的建议再改判 → 409（防反复翻转），非服务端错误。
		if errors.Is(err, ErrInsightAlreadyResolved) {
			return errs.Conflict(c, "该 AI 建议已被采纳/拒绝，不能重复改判")
		}
		// B25 归一：不存在的 ai_insight → 404 not_found（而非 500）。
		return errs.FailNotFound(c, nil, err, "ai insight")
	}
	// S11 留痕：谁在何时 accept/reject 了哪条 AI 建议（审计总线）。
	h.auditResolve(c, actorID, insightID, req.Accepted)
	// 返回改判后的终态 status（accepted / applied / rejected），前端据此呈现生命周期：
	// accept 若触发实际应用（如 severity 改动）会是 applied，否则 accepted。
	return c.JSON(http.StatusOK, map[string]any{
		"status":         "resolved",
		"accepted":       req.Accepted,
		"insight_status": string(ins.Status),
	})
}

// auditResolve 记录 AI 建议改判审计（S11）。db 为 nil / 未注入 recorder 时跳过。
func (h *Handler) auditResolve(c *echo.Context, actorID, insightID int, accepted bool) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), actorID, "")
	e.Action = auth.ActionAIInsightResolve
	e.ResourceType = "ai_insight"
	e.ResourceID = insightID
	e.Detail = map[string]any{"accepted": accepted}
	h.audit.MustRecord(c.Request().Context(), e)
}
