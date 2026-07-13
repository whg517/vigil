// delivery_task.go 通知投递 Asynq 任务（ADR-0017 修订：把「失败走 Asynq 指数退避重试、
// 幂等键 notification_id」的宣称真正落地）。
//
// 链路：deliverChain → 落 pending 行 → enqueue TaskDeliver（TaskID=notif:{行 ID}）
// → DeliveryWorker.Handle 执行降级链：
//   - 送达成功 → 行置 sent，任务完成
//   - 瞬时失败 → return error，asynq 指数退避重试（MaxRetry=deliverMaxRetry）
//   - 重试耗尽/无可用通道 → 行置 failed + allFailedHook 兜底告警，任务进 archived 死信
//
// 幂等：worker 开头按行状态守卫——非 pending（已 sent/failed/suppressed）说明已处理过，
// at-least-once 重投直接跳过，不重复打扰。
package notification

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kevin/vigil/ent"

	"github.com/hibiken/asynq"
)

const (
	// TaskDeliver 通知投递任务类型名。
	TaskDeliver = "vigil:notification:deliver"

	// deliverMaxRetry 投递任务重试上限。
	//
	// 取 5 而非升级任务的 25：通知的价值随时间衰减——asynq 默认退避（约 n⁴ 秒）下
	// 5 次重试覆盖 ~15-20 分钟窗口，足以熬过出口网络闪断/通道服务抖动；再往后
	// 「迟到的升级通知」大概率已被升级链的 repeat/下一 level（各自独立任务）取代，
	// 继续重试只会在事后送达一条过时打扰。链的连续性由 escalation 保证，单条通知
	// 不必无限坚持。
	deliverMaxRetry = 5
)

// deliveryTask 投递任务 payload。标题/正文在入队前已完成模板渲染（重试期间内容稳定，
// 不随模板/规则变更漂移）；incident 上下文只存 ID，worker 执行时重取（IM 卡片渲染需要）。
type deliveryTask struct {
	NotificationID int      `json:"notification_id"` // tracking 行 ID（幂等键）
	IncidentID     int      `json:"incident_id"`     // 0=无单（聚合 flush/兜底）
	Target         Target   `json:"target"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	ActionURL      string   `json:"action_url,omitempty"`
	Channels       []string `json:"channels"` // 已解析的降级链（有序）
	Level          int      `json:"level"`
	Severity       string   `json:"severity"`
}

// deliveryTaskID 生成任务幂等 ID：notif:{notification_id}。
func deliveryTaskID(notifID int) string {
	return fmt.Sprintf("notif:%d", notifID)
}

// TaskEnqueuer 任务入队接口（*asynq.Client 满足），抽象出来便于单测注入 fake。
type TaskEnqueuer interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// getRetryCount / getMaxRetry 包级间接层：asynq 的任务上下文只能由其 server 构造，
// 单测无法伪造，故经变量注入以便测试覆盖「非最后一次 / 最后一次重试」两条路径。
var (
	getRetryCount = asynq.GetRetryCount
	getMaxRetry   = asynq.GetMaxRetry
)

// DeliveryWorker 通知投递任务消费者。wire 注册到 queue：
//
//	q.Register(notification.TaskDeliver, notification.NewDeliveryWorker(db, notifier).Handle)
type DeliveryWorker struct {
	db       *ent.Client
	notifier *Notifier
}

// NewDeliveryWorker 创建投递任务消费者。notifier 须已 SetAsyncDelivery（提供 deliveryStore）。
func NewDeliveryWorker(db *ent.Client, n *Notifier) *DeliveryWorker {
	return &DeliveryWorker{db: db, notifier: n}
}

// Handle 执行一次投递尝试。
//
// 返回 error 的语义（asynq 契约）：
//   - nil：任务完成（已送达 / 幂等跳过 / 行已被清理）
//   - 普通 error：瞬时失败，asynq 按指数退避重试；最后一次重试仍失败则进 archived 死信
//   - 包 asynq.SkipRetry 的 error：确定性失败（无可用通道等），直接进 archived 死信
func (w *DeliveryWorker) Handle(ctx context.Context, task *asynq.Task) error {
	var p deliveryTask
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		// 载荷损坏是确定性失败，重试无益：直接进死信留证。
		return fmt.Errorf("unmarshal delivery task: %w: %w", err, asynq.SkipRetry)
	}
	store := w.notifier.deliveryStore
	if store == nil {
		return fmt.Errorf("delivery worker: async delivery not wired: %w", asynq.SkipRetry)
	}

	// ① 幂等守卫：行已离开 pending（sent/failed/suppressed）= 本通知已处理过，
	// at-least-once 重投直接跳过（防重复送达/重复兜底告警）。
	st, err := store.Status(ctx, p.NotificationID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil // 行已被保留策略清理：过期任务，静默丢弃
		}
		return fmt.Errorf("load notification %d: %w", p.NotificationID, err) // DB 抖动：交给重试
	}
	if st != StatusPending {
		return nil
	}

	// ② 重取 incident 上下文（IM 卡片/webhook 载荷渲染需要）；已被删则降级为无单投递
	//（IM 通道自动跳过，链上其余通道照常）。
	var inc *ent.Incident
	if p.IncidentID > 0 {
		inc, err = w.db.Incident.Get(ctx, p.IncidentID)
		if err != nil {
			if !ent.IsNotFound(err) {
				return fmt.Errorf("get incident %d: %w", p.IncidentID, err)
			}
			inc = nil
		}
	}
	msg := &Message{
		Incident: inc, Targets: []Target{p.Target}, Level: p.Level,
		Title: p.Title, Summary: p.Summary, ActionURL: p.ActionURL, Channels: p.Channels,
	}

	// ③ 最后一次尝试判定：只有 final 失败才落 failed + allFailedHook（每轮重试都触发
	// 会轰炸 org_admin）。取不到 asynq 上下文（理论不发生）时按 final 处理——退化为
	// 旧的一次性尽力语义，宁可少重试不可让行悬在 pending。
	final := true
	if retried, ok := getRetryCount(ctx); ok {
		if maxRetry, ok2 := getMaxRetry(ctx); ok2 {
			final = retried >= maxRetry
		}
	}

	delivered, retryable, reason := w.notifier.deliverTracked(ctx, p.NotificationID, inc, msg, p.Target, p.Channels, p.Level, p.Severity, final)
	if delivered {
		return nil
	}
	err = fmt.Errorf("deliver notification %d (target %s): %s", p.NotificationID, targetKey(p.Target), reason)
	if !retryable && !final {
		// 配置性失败（无可用通道）：行已落 failed、hook 已触发，跳过剩余重试直接归档死信。
		return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
	}
	// 瞬时失败：返回错误交给 asynq 指数退避；final 次返回错误使任务进 archived 死信（可查可重放）。
	return err
}
