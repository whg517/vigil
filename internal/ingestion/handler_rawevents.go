// handler_rawevents.go 原始告警（RawEvent）查询与重放端点（T5.5，能力域 02 §B.15/B.10）。
//
// RawEvent 是"先落库再处理"的可靠性底座：每条进来的告警先落 RawEvent，再入归一化队列。
// 状态机（无 processed 态，C11 已澄清）：
//   - received      刚落库，待归一化（正常态很快转 normalized）
//   - normalized    归一化成功，已产出 Event
//   - parse_failed  适配器解析失败（payload 格式错/无适配器），需人工介入或修正后重放
//   - requeued      入队失败 / 限流 / 背压 落库待回灌（Asynq 巡检任务自动补投，见 sweeper.go）
//
// 本文件提供接入排障面：
//   - GET  /raw-events?integration_id=&status=   查分布/明细（团队软隔离）
//   - POST /raw-events/:id/replay                 重新投入归一化（幂等：Event 唯一索引兜底）
package ingestion

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/ent/rawevent"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// RawEventHandler 原始告警查询/重放 API（接入运维，T5.5）。
type RawEventHandler struct {
	db     *ent.Client
	ingest *Handler            // 复用 enqueueNormalize（重放 = 重新投入归一化队列）
	authz  *auth.Authorizer    // 资源级鉴权（团队软隔离，可选注入）
	scope  *auth.ScopeResolver // 资源→team 反查（可选注入）
	audit  *auth.AuditRecorder // 重放留痕（可选注入）
}

// NewRawEventHandler 构造 RawEvent handler。ingest 用于重放时复用归一化入队逻辑。
func NewRawEventHandler(db *ent.Client, ingest *Handler) *RawEventHandler {
	return &RawEventHandler{db: db, ingest: ingest}
}

// SetAuthorizer 注入鉴权器（团队软隔离）。为 nil 时降级放行（渐进/单测）。
func (h *RawEventHandler) SetAuthorizer(a *auth.Authorizer) { h.authz = a }

// SetScopeResolver 注入 scope 解析器（配合 SetAuthorizer）。
func (h *RawEventHandler) SetScopeResolver(s *auth.ScopeResolver) { h.scope = s }

// SetAuditRecorder 注入审计记录器（重放留痕）。
func (h *RawEventHandler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载路由。
//
//	GET  /raw-events                 查询（integration_id/status 过滤）
//	POST /raw-events/:id/replay       重放
func (h *RawEventHandler) Register(g *echo.Group) {
	g.GET("/raw-events", h.list)
	g.POST("/raw-events/:id/replay", h.replay)
}

func (h *RawEventHandler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// rawEventView 列表项（不回显原始 payload——可能含敏感内容，且体量大；明细排障用 error/headers）。
type rawEventView struct {
	ID            int               `json:"id"`
	Status        string            `json:"status"`
	Error         string            `json:"error,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	IntegrationID int               `json:"integration_id,omitempty"`
	ReceivedAt    time.Time         `json:"received_at"`
	CreatedAt     time.Time         `json:"created_at"`
}

// listResp 查询响应：状态分布（statuses）+ 明细列表（items）。
type listResp struct {
	// Counts 按状态计数（received/normalized/parse_failed/requeued 分布），供排障概览。
	Counts map[string]int `json:"counts"`
	Items  []rawEventView `json:"items"`
}

// list 查询 RawEvent（integration_id / status 过滤 + 状态分布计数）。
//
// @Summary      查询原始告警（RawEvent）
// @Description  按 integration_id/status 过滤，返回 parse_failed/received/requeued 等状态分布 + 明细。团队软隔离——team 级用户仅见本团队接入点的 raw_event。
// @Tags         ingestion
// @Produce      json
// @Param        integration_id  query  int     false  "接入点 ID 过滤"
// @Param        status          query  string  false  "状态过滤（received/normalized/parse_failed/requeued）"
// @Param        limit           query  int     false  "明细条数上限（默认 50，最大 200）"
// @Success      200  {object} listResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /raw-events [get]
func (h *RawEventHandler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.RawEvent.Query()

	// 团队软隔离：team 级用户仅见本团队接入点的 raw_event（经 integration.team 过滤）。
	if h.authz != nil {
		uid := h.actorFromContext(c)
		if uid > 0 {
			teamIDs, orgWide, err := h.authz.VisibleTeamIDs(ctx, uid)
			if err != nil {
				return errs.Internal(c, nil, err)
			}
			if !orgWide {
				if len(teamIDs) == 0 {
					return c.JSON(http.StatusOK, listResp{Counts: map[string]int{}, Items: []rawEventView{}})
				}
				q = q.Where(rawevent.HasIntegrationWith(integration.HasTeamWith(team.IDIn(teamIDs...))))
			}
		}
	}

	// integration_id 过滤
	if s := c.QueryParam("integration_id"); s != "" {
		id, err := strconv.Atoi(s)
		if err != nil {
			return errs.BadRequest(c, "invalid integration_id")
		}
		q = q.Where(rawevent.HasIntegrationWith(integration.IDEQ(id)))
	}
	// status 过滤（校验枚举合法，防注入无效值）
	if s := c.QueryParam("status"); s != "" {
		if !validRawStatus(s) {
			return errs.BadRequest(c, "invalid status")
		}
		q = q.Where(rawevent.StatusEQ(rawevent.Status(s)))
	}

	// 状态分布计数（在过滤 scope + integration 基础上，按 status 分组统计，不受 status 过滤限制）。
	counts, err := h.countByStatus(c, q.Clone())
	if err != nil {
		return errs.Internal(c, nil, err)
	}

	limit := 50
	if s := c.QueryParam("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := q.Order(ent.Desc(rawevent.FieldReceivedAt)).Limit(limit).WithIntegration().All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	items := make([]rawEventView, 0, len(rows))
	for _, r := range rows {
		v := rawEventView{
			ID:         r.ID,
			Status:     r.Status.String(),
			Error:      r.Error,
			Headers:    r.Headers,
			ReceivedAt: r.ReceivedAt,
			CreatedAt:  r.CreatedAt,
		}
		if r.Edges.Integration != nil {
			v.IntegrationID = r.Edges.Integration.ID
		}
		items = append(items, v)
	}
	return c.JSON(http.StatusOK, listResp{Counts: counts, Items: items})
}

// countByStatus 在给定查询（已含 scope/integration 过滤，但应剥离 status 过滤）基础上按状态计数。
// 注：传入的 q 若已加 status 过滤，counts 只会含该状态——调用方须传剥离 status 前的 clone。
// 为简单起见，这里对每个状态各查一次 Count（4 个状态，可接受；避免 GroupBy 的样板）。
func (h *RawEventHandler) countByStatus(c *echo.Context, base *ent.RawEventQuery) (map[string]int, error) {
	ctx := c.Request().Context()
	out := map[string]int{}
	for _, st := range allRawStatuses {
		n, err := base.Clone().Where(rawevent.StatusEQ(st)).Count(ctx)
		if err != nil {
			return nil, err
		}
		out[st.String()] = n
	}
	return out, nil
}

// replay 重放一条 RawEvent：重新投入归一化队列（T5.5）。
//
// 适用：parse_failed（修正适配器/payload 后重试）、requeued（回灌）、received（卡住的重推）。
// 幂等：归一化落 Event 时 (source, source_event_id, status) 唯一索引兜底，重复重放不产重复 Event。
// 重放前把状态重置回 received，避免与巡检回灌/后续查询语义冲突。
//
// @Summary      重放原始告警
// @Description  把指定 RawEvent 重新投入归一化队列（parse_failed/requeued/received 均可）。幂等——重复重放不产重复 Event。
// @Tags         ingestion
// @Produce      json
// @Param        id   path   int  true  "RawEvent ID"
// @Success      202  {object} httputil.AckResponse
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      403  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Failure      503  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /raw-events/{id}/replay [post]
func (h *RawEventHandler) replay(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}

	// 资源级鉴权：对该 raw_event 归属接入点需 raw_event.replay（团队软隔离）。
	if h.authz != nil && h.scope != nil {
		uid := h.actorFromContext(c)
		allowed, aerr := auth.CheckResourceAccess(ctx, h.authz, h.scope, uid, auth.PermRawEventReplay, "raw_event", id)
		if aerr != nil {
			return errs.Internal(c, nil, aerr)
		}
		if !allowed {
			return errs.Forbidden(c, "")
		}
	}

	raw, err := h.db.RawEvent.Query().Where(rawevent.IDEQ(id)).WithIntegration().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return errs.NotFound(c, "raw_event not found")
		}
		return errs.Internal(c, nil, err)
	}
	integ := raw.Edges.Integration
	if integ == nil {
		return errs.BadRequest(c, "raw_event has no integration, cannot replay")
	}

	// queue 未装配时无法重放（应在装配层保证；测试桩会跳过）。
	if h.ingest == nil || h.ingest.queue == nil {
		return errs.Internal(c, nil, nil, "queue not available for replay")
	}

	// 重置状态回 received（清 error），再入队。若入队失败标记 requeued（等巡检回灌）。
	if err := h.db.RawEvent.UpdateOneID(id).
		SetStatus(rawevent.StatusReceived).
		SetError("").
		Exec(ctx); err != nil {
		return errs.Internal(c, nil, err)
	}
	if err := h.ingest.enqueueNormalize(ctx, raw.ID, integ.ID, integ.Type.String()); err != nil {
		_ = h.db.RawEvent.UpdateOneID(id).
			SetStatus(rawevent.StatusRequeued).
			SetError("replay enqueue failed: " + err.Error()).
			Exec(ctx)
		return errs.Internal(c, nil, err, "enqueue replay failed")
	}

	// 重放留痕（谁重放了哪条——会产生新 Event/触发分诊，属写动作）。
	if h.audit != nil {
		uid := h.actorFromContext(c)
		e := auth.AuditEntryFromRequest(c.Request(), uid, "")
		e.Action = auth.ActionRawEventReplay
		e.ResourceType = "raw_event"
		e.ResourceID = id
		h.audit.MustRecord(ctx, e)
	}

	return c.JSON(http.StatusAccepted, httputil.AckResponse{Status: "replaying", RawEventID: id})
}

// allRawStatuses RawEvent 全部状态（用于分布计数）。
var allRawStatuses = []rawevent.Status{
	rawevent.StatusReceived,
	rawevent.StatusNormalized,
	rawevent.StatusParseFailed,
	rawevent.StatusRequeued,
}

// validRawStatus 校验 status 过滤值是否合法枚举。
func validRawStatus(s string) bool {
	for _, st := range allRawStatuses {
		if st.String() == s {
			return true
		}
	}
	return false
}
