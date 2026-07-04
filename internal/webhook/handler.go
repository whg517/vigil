// handler.go 出站 webhook 投递（死信）查询/重放端点（T5.2，C24）。
//
// 出站 webhook 全部重试失败会落 WebhookDelivery(status=failed) 死信。本文件提供运维面：
//   - GET  /webhook-deliveries?status=&incident_id=   查投递记录/死信（状态分布 + 明细）
//   - POST /webhook-deliveries/:id/replay             重放一条死信（原样重发存储的 payload）
//
// 归属：出站 URL 是全局配置式订阅（非 team 资源），故这两个端点用 org 级权限点闸门
// （webhook_delivery.view / webhook_delivery.replay），不走 team 软隔离（无 scope resolver）。
package webhook

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/webhookdelivery"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// Handler 出站 webhook 投递查询/重放 API（接入运维，T5.2）。
type Handler struct {
	db    *ent.Client
	disp  *Dispatcher         // 复用出站签名/单发逻辑（重放）
	audit *auth.AuditRecorder // 重放留痕（可选注入）
}

// NewHandler 构造 handler。disp 用于重放时复用出站投递（含签名）逻辑。
func NewHandler(db *ent.Client, disp *Dispatcher) *Handler {
	return &Handler{db: db, disp: disp}
}

// SetAuditRecorder 注入审计记录器（重放留痕）。
func (h *Handler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载路由。
//
//	GET  /webhook-deliveries               查询（status/incident_id 过滤 + 分布计数）
//	POST /webhook-deliveries/:id/replay     重放死信
func (h *Handler) Register(g *echo.Group) {
	g.GET("/webhook-deliveries", h.list)
	g.POST("/webhook-deliveries/:id/replay", h.replay)
}

func (h *Handler) actorFromContext(c *echo.Context) int {
	if uid, ok := auth.UserIDFromContext(c.Request().Context()); ok {
		return uid
	}
	return 0
}

// deliveryView 列表项（不回显 payload——体量可能大；排障看 event/url/error/status_code 足够）。
type deliveryView struct {
	ID             int       `json:"id"`
	URL            string    `json:"url"`
	Event          string    `json:"event"`
	IncidentID     int       `json:"incident_id,omitempty"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"last_error,omitempty"`
	LastStatusCode int       `json:"last_status_code,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// listResp 查询响应：状态分布（counts）+ 明细（items）。
type listResp struct {
	// Counts 按状态计数（success/failed），供死信概览。
	Counts map[string]int `json:"counts"`
	Items  []deliveryView `json:"items"`
}

// list 查询出站投递记录（status/incident_id 过滤 + 状态分布计数）。
//
// @Summary      查询出站 webhook 投递记录
// @Description  按 status/incident_id 过滤，返回 success/failed（死信）状态分布 + 明细。用于排查出站送达与重放死信。
// @Tags         webhook
// @Produce      json
// @Param        status       query  string  false  "状态过滤（success/failed）"
// @Param        incident_id  query  int     false  "关联 incident 过滤"
// @Param        limit        query  int     false  "明细条数上限（默认 50，最大 200）"
// @Success      200  {object} listResp
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-deliveries [get]
func (h *Handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.WebhookDelivery.Query()

	if s := c.QueryParam("incident_id"); s != "" {
		id, err := strconv.Atoi(s)
		if err != nil {
			return errs.BadRequest(c, "invalid incident_id")
		}
		q = q.Where(webhookdelivery.IncidentIDEQ(id))
	}
	if s := c.QueryParam("status"); s != "" {
		if !validDeliveryStatus(s) {
			return errs.BadRequest(c, "invalid status")
		}
		q = q.Where(webhookdelivery.StatusEQ(webhookdelivery.Status(s)))
	}

	// 状态分布计数（在 incident 过滤基础上、剥离 status 过滤，故用 clone）。
	counts, err := h.countByStatus(ctx, q.Clone())
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

	rows, err := q.Order(ent.Desc(webhookdelivery.FieldCreatedAt)).Limit(limit).All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	items := make([]deliveryView, 0, len(rows))
	for _, r := range rows {
		items = append(items, deliveryView{
			ID:             r.ID,
			URL:            r.URL,
			Event:          r.Event,
			IncidentID:     r.IncidentID,
			Status:         r.Status.String(),
			Attempts:       r.Attempts,
			LastError:      r.LastError,
			LastStatusCode: r.LastStatusCode,
			CreatedAt:      r.CreatedAt,
			UpdatedAt:      r.UpdatedAt,
		})
	}
	return c.JSON(http.StatusOK, listResp{Counts: counts, Items: items})
}

// countByStatus 按状态计数（传入 q 应已剥离 status 过滤）。
func (h *Handler) countByStatus(ctx context.Context, base *ent.WebhookDeliveryQuery) (map[string]int, error) {
	out := map[string]int{}
	for _, st := range allDeliveryStatuses {
		n, err := base.Clone().Where(webhookdelivery.StatusEQ(st)).Count(ctx)
		if err != nil {
			return nil, err
		}
		out[st.String()] = n
	}
	return out, nil
}

// replay 重放一条投递记录：原样重发存储的 payload（复用出站签名）。
//
// 适用死信（status=failed）重投；成功的记录也允许重放（幂等语义由接收端负责）。
// 同步单发（即时反馈成败）：成功则把记录置 success，失败则更新 last_error/last_status_code
// 并累加 attempts 供后续判断。重放本身留痕（谁重放了哪条）。
//
// @Summary      重放出站 webhook 投递
// @Description  把指定投递记录的 payload 原样重发到其 URL（复用出站签名）。用于死信补投。
// @Tags         webhook
// @Produce      json
// @Param        id   path   int  true  "WebhookDelivery ID"
// @Success      200  {object} httputil.AckResponse
// @Failure      400  {object} httputil.ErrorResponse
// @Failure      404  {object} httputil.ErrorResponse
// @Failure      500  {object} httputil.ErrorResponse
// @Failure      502  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /webhook-deliveries/{id}/replay [post]
func (h *Handler) replay(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return errs.BadRequest(c, "invalid id")
	}

	rec, err := h.db.WebhookDelivery.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errs.NotFound(c, "webhook delivery not found")
		}
		return errs.Internal(c, nil, err)
	}
	if h.disp == nil {
		return errs.Internal(c, nil, nil, "dispatcher not available for replay")
	}

	res := h.disp.SendOnce(ctx, rec.URL, rec.Payload)

	// 回写记录：累加 attempts，成功置 success 清错，失败更新错误/状态码。
	upd := h.db.WebhookDelivery.UpdateOneID(id).AddAttempts(1)
	if res.Success {
		upd = upd.SetStatus(webhookdelivery.StatusSuccess).SetLastError("").SetLastStatusCode(res.StatusCode)
	} else {
		errStr := ""
		if res.Err != nil {
			errStr = res.Err.Error()
		} else {
			errStr = "status " + strconv.Itoa(res.StatusCode)
		}
		upd = upd.SetStatus(webhookdelivery.StatusFailed).SetLastError(errStr).SetLastStatusCode(res.StatusCode)
	}
	if err := upd.Exec(ctx); err != nil {
		return errs.Internal(c, nil, err)
	}

	// 重放留痕（谁重放了哪条死信）。
	if h.audit != nil {
		e := auth.AuditEntryFromRequest(c.Request(), h.actorFromContext(c), "")
		e.Action = auth.ActionWebhookDeliveryReplay
		e.ResourceType = "webhook_delivery"
		e.ResourceID = id
		e.Detail["success"] = res.Success
		if !res.Success {
			e.Result = auth.AuditResultFailed
		}
		h.audit.MustRecord(ctx, e)
	}

	if !res.Success {
		// 重放本身执行成功（记录已回写），但目标端仍未接住——502 让调用方知晓未送达。
		return c.JSON(http.StatusBadGateway, httputil.AckResponse{Status: "replay_failed", ID: id})
	}
	return c.JSON(http.StatusOK, httputil.AckResponse{Status: "replayed", ID: id})
}

// allDeliveryStatuses WebhookDelivery 全部状态（用于分布计数）。
var allDeliveryStatuses = []webhookdelivery.Status{
	webhookdelivery.StatusSuccess,
	webhookdelivery.StatusFailed,
}

// validDeliveryStatus 校验 status 过滤值是否合法枚举。
func validDeliveryStatus(s string) bool {
	for _, st := range allDeliveryStatuses {
		if st.String() == s {
			return true
		}
	}
	return false
}
