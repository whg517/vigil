// Package ingestion 实现能力域 1-2：告警接入与归一化。
//
// 对应 docs/capabilities/01-ingestion-normalization.md：
// · 接收与处理解耦 —— Receiver 秒级落 RawEvent 并入队，归一化在 Asynq worker 异步执行
// · 不丢告警 —— 限流/背压时 payload 仍落库
// · 幂等 —— source_event_id 作幂等键
//
// 本包包含：webhook 接收 handler、Adapter 接口与注册表、归一化 worker。
package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/ent/rawevent"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/triage"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v4"
)

// Handler 处理告警 webhook 接入。
// 接收 → 鉴权（token）→ 落 RawEvent → 入归一化队列 → 秒级返回 202。
type Handler struct {
	db    *ent.Client
	queue *queue.Queue
}

// NewHandler 创建接入 handler。
func NewHandler(db *ent.Client, q *queue.Queue) *Handler {
	return &Handler{db: db, queue: q}
}

// Register 把 webhook 路由挂到 Echo group。
// 路由：POST /webhook/:token —— token 即接入点鉴权凭证（对应 Integration.token）。
func (h *Handler) Register(g *echo.Group) {
	g.POST("/webhook/:token", h.receiveWebhook)
}

// receiveWebhook 处理通用 webhook 接入。
func (h *Handler) receiveWebhook(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return c.JSON(http.StatusUnauthorized, errMsg("missing token"))
	}

	// 1. 按 token 查接入点（含 enabled 校验）
	integ, err := h.db.Integration.Query().
		Where(integration.TokenEQ(token), integration.EnabledEQ(true)).
		Only(c.Request().Context())
	if err != nil {
		// 不暴露"不存在"与"未启用"的区别，统一 401
		return c.JSON(http.StatusUnauthorized, errMsg("invalid token"))
	}

	// 2. 读取 payload（限制大小，防滥用）
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxPayloadBytes))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errMsg("read body: "+err.Error()))
	}

	// 3. 落 RawEvent（先落库，保证不丢）
	raw, err := h.db.RawEvent.Create().
		SetPayload(body).
		SetHeaders(extractHeaders(c.Request())).
		SetStatus(rawevent.StatusReceived).
		SetIntegration(integ).
		Save(c.Request().Context())
	if err != nil {
		// 落库失败是最严重的——告警可能丢失。返回 5xx 让告警源重试。
		return c.JSON(http.StatusInternalServerError, errMsg("persist failed"))
	}

	// 4. 入归一化队列（异步处理）
	taskPayload, _ := json.Marshal(normalizePayload{
		RawEventID:    raw.ID,
		IntegrationID: integ.ID,
		SourceType:    integ.Type.String(),
	})
	if _, err := h.queue.Client.Enqueue(
		asynq.NewTask(TaskNormalize, taskPayload),
		asynq.Queue("default"),
	); err != nil {
		// 入队失败：RawEvent 已落库，标记 requeued 等待回灌（能力域 01 §6）
		_ = h.db.RawEvent.UpdateOneID(raw.ID).
			SetStatus(rawevent.StatusRequeued).
			SetError("enqueue failed: " + err.Error()).
			Exec(c.Request().Context())
		// 仍返回 202——告警源不必重试，Vigil 会从 RawEvent 回灌
	}

	// 5. 秒级返回 202 Accepted
	return c.JSON(http.StatusAccepted, map[string]any{
		"status":       "accepted",
		"raw_event_id": raw.ID,
	})
}

// extractHeaders 提取关键请求头（用于审计与排查）。
func extractHeaders(r *http.Request) map[string]string {
	return map[string]string{
		"User-Agent":      r.Header.Get("User-Agent"),
		"Content-Type":    r.Header.Get("Content-Type"),
		"X-Forwarded-For": r.RemoteAddr,
	}
}

// errMsg 构造错误响应体。
func errMsg(msg string) map[string]any {
	return map[string]any{"error": msg}
}

// NormalizeWorker 归一化 worker：消费归一化任务，把 RawEvent 转成 Event。
type NormalizeWorker struct {
	db       *ent.Client
	registry *AdapterRegistry
	queue    *queue.Queue // 用于归一化成功后入队分诊任务
}

// NewNormalizeWorker 创建归一化 worker。q 可为 nil（测试时跳过分诊入队）。
func NewNormalizeWorker(db *ent.Client, r *AdapterRegistry, q *queue.Queue) *NormalizeWorker {
	return &NormalizeWorker{db: db, registry: r, queue: q}
}

// Handle 处理归一化任务（注册到 queue）。
func (w *NormalizeWorker) Handle(ctx context.Context, t *asynq.Task) error {
	var p normalizePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// payload 格式错误不可重试，标记失败
		return fmt.Errorf("unmarshal normalize payload: %w", err)
	}

	// 1. 取 RawEvent
	raw, err := w.db.RawEvent.Get(ctx, p.RawEventID)
	if err != nil {
		return fmt.Errorf("get raw_event %d: %w", p.RawEventID, err)
	}

	// 2. 选适配器
	adapter, ok := w.registry.Get(p.SourceType)
	if !ok {
		// 无适配器：标记 parse_failed，人工介入
		return w.failRaw(ctx, raw.ID, fmt.Sprintf("no adapter for source type %q", p.SourceType))
	}

	// 3. 归一化
	// 注：完整实现需查 Integration 实体的 service 绑定与归一化配置，
	// 此处先做核心：payload → Event。
	integ, err := w.db.Integration.Get(ctx, p.IntegrationID)
	if err != nil {
		return w.failRaw(ctx, raw.ID, "get integration: "+err.Error())
	}

	evt, err := adapter.Normalize(ctx, raw.Payload, integ, raw)
	if err != nil {
		return w.failRaw(ctx, raw.ID, "normalize: "+err.Error())
	}

	// 4. 落 Event（幂等：source + source_event_id 唯一索引保证）
	eventCreate := w.db.Event.Create().
		SetSourceEventID(evt.SourceEventID).
		SetSource(evt.Source).
		SetSeverity(event.Severity(evt.Severity)).
		SetStatus(event.Status(evt.Status)).
		SetSummary(evt.Summary).
		SetDetail(evt.Detail).
		SetLabels(evt.Labels).
		SetDedupKey(evt.DedupKey).
		SetIntegration(integ)
	created, err := eventCreate.Save(ctx)
	if err != nil {
		// 幂等冲突（重复推送）视为成功，不再触发分诊
		if ent.IsConstraintError(err) {
			return w.markRawNormalized(ctx, raw.ID, 0)
		}
		return fmt.Errorf("save event: %w", err)
	}
	// 埋点：告警接入量（按 source/severity）
	metrics.AlertsReceived.WithLabelValues(evt.Source, evt.Severity).Inc()

	// 5. RawEvent 标记 normalized
	if err := w.markRawNormalized(ctx, raw.ID, created.ID); err != nil {
		return err
	}

	// 6. 入队分诊任务（能力域 3），流水线串接
	if w.queue != nil {
		task, err := triage.EnqueueTask(created.ID)
		if err != nil {
			return fmt.Errorf("build triage task: %w", err)
		}
		if _, err := w.queue.Client.Enqueue(task, asynq.Queue("default")); err != nil {
			// 入队失败不阻塞归一化（Event 已落库，可由巡检任务回灌分诊）
			return fmt.Errorf("enqueue triage: %w", err)
		}
	}
	return nil
}

// failRaw 标记 RawEvent 为 parse_failed 并记录错误。
func (w *NormalizeWorker) failRaw(ctx context.Context, rawID int, errMsg string) error {
	return w.db.RawEvent.UpdateOneID(rawID).
		SetStatus(rawevent.StatusParseFailed).
		SetError(errMsg).
		Exec(ctx)
}

// markRawNormalized 标记 RawEvent 已归一化。
func (w *NormalizeWorker) markRawNormalized(ctx context.Context, rawID, eventID int) error {
	return w.db.RawEvent.UpdateOneID(rawID).
		SetStatus(rawevent.StatusNormalized).
		Exec(ctx)
}

const (
	// TaskNormalize 归一化任务类型名。
	TaskNormalize = "vigil:normalize"

	// maxPayloadBytes 单个 webhook payload 上限（1MB）。
	maxPayloadBytes = 1 << 20
)

// normalizePayload 归一化任务 payload。
type normalizePayload struct {
	RawEventID    int    `json:"raw_event_id"`
	IntegrationID int    `json:"integration_id"`
	SourceType    string `json:"source_type"`
}

// 保留 time 引用（后续扩展超时控制用）。
var _ = time.Now
