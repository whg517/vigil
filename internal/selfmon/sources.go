// sources.go 自监控数据来源的生产实现（asynq Inspector / ent 查询）。
//
// 与 selfmon.go 的接口分离：接口便于测试注入 fake；本文件是接线到真实基础设施的适配器，
// 由 wire.go 构造后注入 Engine。
package selfmon

import (
	"context"
	"time"

	"github.com/hibiken/asynq"

	"github.com/kevin/vigil/ent"
	entnotification "github.com/kevin/vigil/ent/notification"
)

// inspectorQueueSource 用 asynq Inspector 汇总各队列积压深度（pending+active）。
type inspectorQueueSource struct {
	insp *asynq.Inspector
}

// NewInspectorQueueSource 从 asynq Inspector 构造队列深度来源。insp 为 nil 时返回 nil
// （wire 侧据此降级——无 Redis/Inspector 时不做队列检查）。
func NewInspectorQueueSource(insp *asynq.Inspector) QueueDepthSource {
	if insp == nil {
		return nil
	}
	return &inspectorQueueSource{insp: insp}
}

// Depth 汇总所有队列的 pending+active 之和。
//
// 只算 pending+active（待消费 + 正在消费的实时堆积），不含 scheduled（升级计时器等按计划
// 延迟任务，正常态就大量存在，计入会误报）/retry/archived。任一队列 GetQueueInfo 失败则
// 整体返回 error（探测失败让引擎跳过本轮，不误报）。
func (s *inspectorQueueSource) Depth(_ context.Context) (int, error) {
	queues, err := s.insp.Queues()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, q := range queues {
		info, err := s.insp.GetQueueInfo(q)
		if err != nil {
			return 0, err
		}
		total += info.Pending + info.Active
	}
	return total, nil
}

// entFailureRate 从 Notification 表统计业务通知失败率。
type entFailureRate struct {
	db *ent.Client
}

// NewEntFailureRate 从 ent 客户端构造失败率来源。db 为 nil 时返回 nil（wire 侧据此降级）。
func NewEntFailureRate(db *ent.Client) FailureRateSource {
	if db == nil {
		return nil
	}
	return &entFailureRate{db: db}
}

// Rate 统计 [now-window, now] 内业务通知的 (failed, total)。
//
// ★ 防递归边界（见 selfmon.go 包注释红线 2）：只统计「关联了真实 Incident 的通知」
// （HasIncident），即 escalation/targeted 等正常业务送达。自监控自告警走 NotifyUnrouted、
// 无 Incident 关联（incident_id=0），故天然被 HasIncident 排除——自告警即便失败也不会抬高
// 本失败率，杜绝「自告警失败 → 失败率升高 → 再触发 → 循环」。
//
// 同理，其它 unrouted 兜底告警（空班/全败兜底）也不计入本「业务通知失败率」——它们本身
// 是「元告警」，把元告警的失败混进业务失败率会稀释/污染信号，故此边界既防递归也更纯粹。
//
// total 计入 sent+failed（成功+失败），不含 suppressed（静默拦截是「按策略不发」，非失败）
// 与 pending（在途未定型）。
func (r *entFailureRate) Rate(ctx context.Context, window time.Duration) (failed, total int, err error) {
	since := time.Now().Add(-window)
	base := r.db.Notification.Query().
		Where(
			entnotification.HasIncident(),
			entnotification.CreatedAtGTE(since),
			entnotification.StatusIn(entnotification.StatusSent, entnotification.StatusFailed),
		)
	total, err = base.Clone().Count(ctx)
	if err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	failed, err = base.Clone().
		Where(entnotification.StatusEQ(entnotification.StatusFailed)).
		Count(ctx)
	if err != nil {
		return 0, 0, err
	}
	return failed, total, nil
}
