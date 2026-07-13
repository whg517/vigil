// Package selfmon 实现自监控闭环（H2.4）。
//
// 定位：Vigil 是告警处置平台，但它自身也会出故障——队列积压（消费跟不上）、通知
// 发不出去（通道故障）。若这类故障发生时无人告警，平台就是「哑巴」：事故堆着却没人知道。
// 本包周期巡检关键内部信号，超阈值时经「独立通道」自告警给 org_admin，补上「守夜人也
// 需要有人守夜」这一环。
//
// ★ 三条不可动摇的设计红线（诚实 + 防自触发循环 是第一要务）：
//
//  1. 独立于被监控链路。自告警直接经 NotifyUnrouted 独立通道（默认 webhook/email，刻意
//     排除 im）发给 org_admin，绝不进 escalation 流水线。原因：被监控的正是这条流水线——
//     若自告警也走它，「链路坏了 → 告警也走坏链路 → 告警也失败」，等于没告警。
//
//  2. 防递归。自告警自身产生的送达记录必须从「通知失败率」统计里排除，否则：自告警走
//     独立通道若也失败 → 失败记录抬高失败率 → 再次触发 notif_failure 告警 → 又失败 →
//     无限循环。本实现的排除边界见 FailureRateSource（entFailureRate）注释：只统计「关联
//     了真实 Incident 的业务通知」，自监控告警是 unrouted（incident_id=0）故天然被排除。
//
//  3. 诚实边界。若 Enabled 但独立通道未真正配置（Notifier 无可用通道），启动时 log warn
//     明说「自监控开启但独立通道未配，告警可能无法送达」——不假装闭环一定成功。
//
// 冷却：同一 kind（queue_depth / notif_failure / queue_probe_failure）在 Cooldown 内
// 不重复发，防每个 interval 刷屏。
package selfmon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/notification"
)

// AlertKind 自监控告警类别（用于冷却分桶 + metrics 维度）。
type AlertKind string

const (
	// KindQueueDepth 队列积压告警：各队列 pending+active 之和超阈值。
	KindQueueDepth AlertKind = "queue_depth"
	// KindNotifFailure 通知失败率告警：统计窗口内业务通知失败率超阈值。
	KindNotifFailure AlertKind = "notif_failure"
	// KindQueueProbeFailure 队列探测连续失败告警：Depth 探测连续 N 次报错。
	// 与 KindQueueDepth（积压=链路慢了）不同，这是「链路探不到了」——Redis 整体故障时
	// 探测持续报错，若只 warn 不告警，恰在异步链路全停的时刻自监控静默。
	KindQueueProbeFailure AlertKind = "queue_probe_failure"
)

// QueueDepthSource 队列积压深度来源（生产用 asynq Inspector，测试用 fake）。
// Depth 返回「所有队列 pending+active 之和」——积压 = 待消费 + 正在消费的堆积总量。
// 返回 error 时视为「本次探测失败」（跳过该项检查，不触发告警，避免探测抖动误报）。
type QueueDepthSource interface {
	Depth(ctx context.Context) (int, error)
}

// FailureRateSource 通知失败率来源。
// Rate 返回 [window] 窗口内的 (failed, total) 计数——total 为参与统计的业务通知总数，
// failed 为其中失败数。★ 实现必须排除自监控告警自身的送达记录（防递归，见包注释红线 2）。
type FailureRateSource interface {
	Rate(ctx context.Context, window time.Duration) (failed, total int, err error)
}

// AlertNotifier 自告警发送口（独立通道，不进 escalation）。
// 生产实现包装 notification.Notifier.NotifyUnrouted；测试用 fake 断言调用。
type AlertNotifier interface {
	// Alert 经独立通道把一条自监控告警送给收件人（channels 由引擎传入，刻意不含 im）。
	Alert(ctx context.Context, targets []notification.Target, title, summary string, channels []string) error
}

// AdminResolver 解算自告警收件人（org_admin）。生产实现复用 wire 的 org_admin 解算逻辑。
type AdminResolver interface {
	Resolve(ctx context.Context) ([]notification.Target, error)
}

// Config 引擎运行参数（从 config.SelfMonitor 映射，用 Effective* 兜底后传入）。
type Config struct {
	CheckInterval        time.Duration
	QueueDepthThreshold  int
	FailureRateThreshold float64
	FailureRateWindow    time.Duration
	FailureRateMinSample int
	Cooldown             time.Duration
	AlertChannels        []string
	// QueueProbeFailureThreshold 队列探测「连续失败」红线阈值：连续 N 次 Depth 报错才告警。
	// 单次失败仍只 warn（防 Inspector 抖动/Redis 瞬断误报）。<=0 时红线关闭（保持只 warn）。
	QueueProbeFailureThreshold int
}

// Engine 自监控引擎：ticker 周期检查 + 超阈自告警（独立通道 + 冷却 + 防递归）。
type Engine struct {
	cfg      Config
	queue    QueueDepthSource
	failRate FailureRateSource
	notifier AlertNotifier
	admins   AdminResolver
	log      *zap.Logger

	// now 可注入的时钟（测试控制冷却）。nil 时用 time.Now。
	now func() time.Time

	mu       sync.Mutex
	lastSent map[AlertKind]time.Time // 各 kind 上次告警时刻（内存冷却，进程级足够）
	// queueProbeFails 队列探测连续失败计数（探测成功即清零）。与冷却同为进程内存状态：
	// 多副本各自计数/各自告警的现状与冷却一致，本批次不解决（见冷却 map 注释）。
	queueProbeFails int
}

// NewEngine 构造自监控引擎。queue / failRate / notifier / admins 任一为 nil 时对应检查降级
// 跳过（不 panic）——保持「组件缺失即降级」的项目基线。
func NewEngine(cfg Config, queue QueueDepthSource, failRate FailureRateSource, notifier AlertNotifier, admins AdminResolver, log *zap.Logger) *Engine {
	if log == nil {
		log = zap.NewNop()
	}
	return &Engine{
		cfg:      cfg,
		queue:    queue,
		failRate: failRate,
		notifier: notifier,
		admins:   admins,
		log:      log,
		lastSent: make(map[AlertKind]time.Time),
	}
}

// nowFn 返回当前时刻（可注入时钟兜底 time.Now）。
func (e *Engine) nowFn() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Run 启动周期巡检，阻塞到 ctx 取消（纳入优雅关闭）。
// 单次检查内部错误只记日志，不中断 ticker（自监控自身故障不应拖垮进程）。
func (e *Engine) Run(ctx context.Context) {
	interval := e.cfg.CheckInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	e.log.Info("self-monitor engine started",
		zap.Duration("interval", interval),
		zap.Int("queue_depth_threshold", e.cfg.QueueDepthThreshold),
		zap.Float64("failure_rate_threshold", e.cfg.FailureRateThreshold),
		zap.Strings("alert_channels", e.cfg.AlertChannels))
	for {
		select {
		case <-ctx.Done():
			e.log.Info("self-monitor engine stopped")
			return
		case <-ticker.C:
			e.Check(ctx)
		}
	}
}

// Check 执行一轮检查（导出供测试直接驱动，不依赖 ticker）。
// 两项检查相互独立：一项探测失败/降级不影响另一项。
func (e *Engine) Check(ctx context.Context) {
	e.checkQueueDepth(ctx)
	e.checkFailureRate(ctx)
}

// checkQueueDepth 队列积压检查：各队列 pending+active 之和超阈值 → 触发告警。
func (e *Engine) checkQueueDepth(ctx context.Context) {
	if e.queue == nil || e.cfg.QueueDepthThreshold <= 0 {
		return // 无来源或阈值关闭：跳过
	}
	depth, err := e.queue.Depth(ctx)
	if err != nil {
		// 单次探测失败不告「积压」：Inspector 抖动/Redis 瞬断不应误报（防抖动保留）。
		// 但连续失败是另一回事——由 noteQueueProbeFailure 累计，达阈值走独立红线告警。
		e.log.Warn("self-monitor: queue depth probe failed", zap.Error(err))
		e.noteQueueProbeFailure(ctx, err)
		return
	}
	e.resetQueueProbeFailures()
	if depth <= e.cfg.QueueDepthThreshold {
		return
	}
	title := "[自监控] 任务队列积压"
	summary := fmt.Sprintf(
		"当前队列积压 %d（pending+active），超过阈值 %d。消费可能跟不上生产（worker 不足/下游卡顿/Redis 异常），"+
			"升级计时、通知重试、归一化等异步任务将延迟。请核查 Asynq worker 与 Redis。",
		depth, e.cfg.QueueDepthThreshold)
	e.fire(ctx, KindQueueDepth, title, summary)
}

// noteQueueProbeFailure 累计一次队列探测失败，连续达到阈值 → 独立红线告警。
//
// 为什么是独立 kind 而非并入「积压」：积压是「链路慢了」（有数据、超阈值），探测连续失败
// 是「链路探不到了」（无数据）——Redis 整体故障时正属后者，此刻升级计时/通知重试等异步
// 链路可能已全停，恰是自监控最不能静默的时刻。两者症状/处置不同，混为一谈会误导值班人。
//
// 恢复语义与现有红线一致：探测成功即重置计数（resetQueueProbeFailures），告警自然停止，
// 无显式 resolve 通知；持续失败期间的重复告警由 fire 的冷却统一抑制。
func (e *Engine) noteQueueProbeFailure(ctx context.Context, cause error) {
	threshold := e.cfg.QueueProbeFailureThreshold
	if threshold <= 0 {
		return // 红线关闭：维持原「只 warn」行为
	}
	e.mu.Lock()
	e.queueProbeFails++
	fails := e.queueProbeFails
	e.mu.Unlock()
	if fails < threshold {
		return
	}
	title := "[自监控] 队列探测连续失败"
	summary := fmt.Sprintf(
		"队列积压探测已连续 %d 次失败（红线阈值 %d），最近错误：%v。Redis / Asynq 可能整体不可用——"+
			"升级计时、通知重试、归一化等异步链路可能已全停，且积压检查在此期间处于失明状态。"+
			"请立即核查 Redis 连通性与 Asynq worker 状态。",
		fails, threshold, cause)
	e.fire(ctx, KindQueueProbeFailure, title, summary)
}

// resetQueueProbeFailures 队列探测成功后重置连续失败计数。
// 曾达红线的恢复记 info（与「告警自然停止」的解除语义配套，日志侧留下恢复时间点）。
func (e *Engine) resetQueueProbeFailures() {
	e.mu.Lock()
	fails := e.queueProbeFails
	e.queueProbeFails = 0
	e.mu.Unlock()
	if e.cfg.QueueProbeFailureThreshold > 0 && fails >= e.cfg.QueueProbeFailureThreshold {
		e.log.Info("self-monitor: queue probe recovered", zap.Int("consecutive_failures", fails))
	}
}

// checkFailureRate 通知失败率检查：窗口内业务通知 failed/total 超阈值且样本足够 → 触发告警。
func (e *Engine) checkFailureRate(ctx context.Context) {
	if e.failRate == nil || e.cfg.FailureRateThreshold <= 0 {
		return
	}
	window := e.cfg.FailureRateWindow
	if window <= 0 {
		window = 15 * time.Minute
	}
	failed, total, err := e.failRate.Rate(ctx, window)
	if err != nil {
		e.log.Warn("self-monitor: notification failure rate probe failed", zap.Error(err))
		return
	}
	// 样本不足不判：小样本波动（如窗口内仅 2 条、1 条失败=50%）会误报。
	if total < e.cfg.FailureRateMinSample {
		return
	}
	rate := float64(failed) / float64(total)
	if rate <= e.cfg.FailureRateThreshold {
		return
	}
	title := "[自监控] 通知失败率过高"
	summary := fmt.Sprintf(
		"最近 %s 内业务通知失败率 %.0f%%（%d/%d），超过阈值 %.0f%%。多个响应者可能收不到告警，"+
			"请核查通知通道（IM/邮件/电话/短信）配置与外部依赖。",
		window, rate*100, failed, total, e.cfg.FailureRateThreshold*100)
	e.fire(ctx, KindNotifFailure, title, summary)
}

// fire 触发一次自告警：冷却判定 → 解算 org_admin → 经独立通道发 → 记 metrics + 冷却时间。
//
// 冷却：同 kind 在 Cooldown 内已发过则跳过（防每个 interval 刷屏）。
// 独立通道：channels = cfg.AlertChannels（刻意不含 im），直接 NotifyUnrouted 不进 escalation。
func (e *Engine) fire(ctx context.Context, kind AlertKind, title, summary string) {
	if !e.allowFire(kind) {
		e.log.Debug("self-monitor: alert suppressed by cooldown", zap.String("kind", string(kind)))
		return
	}
	if e.notifier == nil || e.admins == nil {
		// 诚实边界：开启了却没接 notifier/解算器——记 warn，不假装发成功。
		e.log.Warn("self-monitor: alert triggered but notifier/admin resolver missing (not delivered)",
			zap.String("kind", string(kind)), zap.String("title", title))
		return
	}
	targets, err := e.admins.Resolve(ctx)
	if err != nil || len(targets) == 0 {
		e.log.Warn("self-monitor: no org_admin to alert",
			zap.String("kind", string(kind)), zap.Error(err))
		return
	}
	if err := e.notifier.Alert(ctx, targets, title, summary, e.cfg.AlertChannels); err != nil {
		// best-effort：发送失败只记日志，不重试不放大（避免故障叠加）。
		e.log.Warn("self-monitor: alert delivery failed",
			zap.String("kind", string(kind)), zap.Error(err))
		// 注意：即便发送失败也记冷却时间——否则每个 interval 都会重试一条发不出去的告警，
		// 反而放大压力；等 Cooldown 后再试。metrics 仍计数（确有触发）。
	}
	e.markFired(kind)
	metrics.SelfMonitorAlerts.WithLabelValues(string(kind)).Inc()
	e.log.Info("self-monitor alert fired",
		zap.String("kind", string(kind)),
		zap.Int("recipients", len(targets)),
		zap.Strings("channels", e.cfg.AlertChannels))
}

// allowFire 判定 kind 是否已过冷却期（可再次告警）。
func (e *Engine) allowFire(kind AlertKind) bool {
	cooldown := e.cfg.Cooldown
	if cooldown <= 0 {
		return true // 冷却关闭：每次超阈都发
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	last, ok := e.lastSent[kind]
	if !ok {
		return true
	}
	return e.nowFn().Sub(last) >= cooldown
}

// markFired 记录 kind 本次告警时刻（用于冷却）。
func (e *Engine) markFired(kind AlertKind) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSent[kind] = e.nowFn()
}
