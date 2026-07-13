// queue.go Asynq 队列指标：按队列维度暴露 pending/active/scheduled/retry/archived gauge。
//
// 背景：architecture.md §7.3 承诺 /metrics 暴露「队列深度」，此前实现只有业务 counters 与
// HTTP 直方图——外部 Prometheus 抓不到队列积压，更看不到死信（archived：升级/通知等任务
// 重试耗尽后的最终失败）。死信在默认部署形态（未开 Asynqmon）下完全不可见，是盲区。
//
// ★ 选型：用现有 asynq Inspector 周期采集写自建 gauge，而非引入官方 x/metrics 的
// QueueMetricsCollector。理由：
//  1. 零新依赖——x/metrics 是独立 module（github.com/hibiken/asynq/x），为一个采集循环
//     引入新依赖不划算；Inspector 本仓库已在多处使用（selfmon / escalation sweeper）。
//  2. 命名一致——自建 gauge 用 vigil_ 前缀与现有指标同族，外部告警规则不必混用 asynq_*。
//  3. 解耦抓取路径——x/metrics 在每次 /metrics 被抓取时同步查 Redis（collect-on-scrape），
//     Redis 故障会拖慢甚至拖挂抓取端点（外部监控最需要 /metrics 的时刻恰是 Redis 出事时）；
//     周期采样把 Redis 依赖隔离在后台 goroutine，/metrics 永远快速返回，
//     代价是最长一个采样间隔的滞后，对告警场景可接受。
package metrics

import (
	"context"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// 队列指标（QueueStatsCollector 周期写入）
var (
	// QueueTasks 各队列分状态任务数 gauge。
	// state ∈ pending|active|scheduled|retry|archived：
	//   - pending+active 是实时积压（消费跟不上生产的信号）；
	//   - scheduled 是按计划延迟的任务（升级计时器等），正常态就大量存在；
	//   - retry 是等待重试的失败任务；
	//   - archived 是死信——重试耗尽的最终失败（升级/通知彻底丢失），>0 即应告警
	//     （告警规则示例见 docs/operations.md「外部监控接入」）。
	QueueTasks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigil_queue_tasks",
		Help: "Number of asynq tasks per queue by state (pending/active/scheduled/retry/archived).",
	}, []string{"queue", "state"})

	// QueueStatsCollectErrors 队列指标采集失败计数。
	// 采集失败时 QueueTasks 保留上次值（陈旧），本计数持续增长即说明 gauge 不可信——
	// 同时它本身也是 Redis 故障的外部可见信号（selfmon 挂在同一进程，进程/Redis 全挂时
	// 只有外部监控能发现，见 docs/operations.md）。
	QueueStatsCollectErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigil_queue_stats_collect_errors_total",
		Help: "Total errors while collecting asynq queue stats (queue gauges may be stale while this grows).",
	})
)

// QueueStatsCollector 周期用 asynq Inspector 拉取各队列分状态计数写入 QueueTasks。
type QueueStatsCollector struct {
	insp     *asynq.Inspector
	interval time.Duration
	log      *zap.Logger

	// failing 上次采集是否失败——只在状态翻转时 warn/info，避免 Redis 长故障期间每个
	// tick 刷一条 warn（错误次数由 QueueStatsCollectErrors 完整记录）。
	// 仅 Run goroutine 读写，无需加锁。
	failing bool
}

// NewQueueStatsCollector 构造队列指标采集器。insp 为 nil 时返回 nil（wire 侧据此降级，
// 与 selfmon.NewInspectorQueueSource 同约定）；interval<=0 回退 15s（常见抓取间隔量级，
// 每 tick 仅 1+N 次 Redis 查询，N=队列数，负载可忽略）。
func NewQueueStatsCollector(insp *asynq.Inspector, interval time.Duration, log *zap.Logger) *QueueStatsCollector {
	if insp == nil {
		return nil
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &QueueStatsCollector{insp: insp, interval: interval, log: log}
}

// Interval 返回生效的采集间隔（供 wire 侧日志）。
func (c *QueueStatsCollector) Interval() time.Duration { return c.interval }

// Run 启动周期采集，阻塞到 ctx 取消（由 wire 的 goPeriodic 纳入优雅关闭）。
func (c *QueueStatsCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.collectOnce() // 启动立即采一次：否则首个 interval 内 /metrics 无队列数据
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectOnce()
		}
	}
}

// collectOnce 执行一轮采集并处理失败：计数 + 状态翻转日志（降噪）。
func (c *QueueStatsCollector) collectOnce() {
	if err := c.collect(); err != nil {
		QueueStatsCollectErrors.Inc()
		if !c.failing {
			c.log.Warn("queue stats collect failed; queue gauges hold last values until recovery", zap.Error(err))
		}
		c.failing = true
		return
	}
	if c.failing {
		c.log.Info("queue stats collect recovered")
	}
	c.failing = false
}

// collect 拉取全部队列的分状态计数写入 gauge，任一步失败返回 error。
// ★ 失败时不清零已有 gauge：清零会被外部监控误读为「积压消失/死信清空」，
// 保留陈旧值 + QueueStatsCollectErrors 增长才是诚实的表达。
func (c *QueueStatsCollector) collect() error {
	queues, err := c.insp.Queues()
	if err != nil {
		return err
	}
	for _, q := range queues {
		info, err := c.insp.GetQueueInfo(q)
		if err != nil {
			return err
		}
		QueueTasks.WithLabelValues(q, "pending").Set(float64(info.Pending))
		QueueTasks.WithLabelValues(q, "active").Set(float64(info.Active))
		QueueTasks.WithLabelValues(q, "scheduled").Set(float64(info.Scheduled))
		QueueTasks.WithLabelValues(q, "retry").Set(float64(info.Retry))
		QueueTasks.WithLabelValues(q, "archived").Set(float64(info.Archived))
	}
	return nil
}
