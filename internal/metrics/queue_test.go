// queue_test.go 队列指标采集器测试（miniredis + 真实 asynq client/Inspector 冒烟）。
//
// 核心断言：
//   - 各队列分状态计数正确写入 vigil_queue_tasks（含死信 archived——本批次要补的盲区）；
//   - 采集失败时 gauge 保留上次值（不清零，防外部监控误读「积压消失」）且错误计数递增。
//
// 注意：gauge 注册在全局默认 registry，测试间用互不相同的队列名隔离，避免交叉污染。
package metrics

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newQueueEnv 起 miniredis + asynq client + Inspector + 采集器。
func newQueueEnv(t *testing.T) (*miniredis.Miniredis, *asynq.Client, *QueueStatsCollector) {
	t.Helper()
	mr := miniredis.RunT(t)
	opt := asynq.RedisClientOpt{Addr: mr.Addr()}
	client := asynq.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	insp := asynq.NewInspector(opt)
	t.Cleanup(func() { _ = insp.Close() })
	c := NewQueueStatsCollector(insp, time.Second, nil)
	if c == nil {
		t.Fatal("collector should not be nil with valid inspector")
	}
	return mr, client, c
}

// gaugeVal 读 vigil_queue_tasks{queue,state} 当前值。
func gaugeVal(t *testing.T, queue, state string) float64 {
	t.Helper()
	return testutil.ToFloat64(QueueTasks.WithLabelValues(queue, state))
}

// TestQueueStatsCollectPerQueuePerState 按队列/状态采集：pending、scheduled 分队列正确，
// 未发生的状态为 0。
func TestQueueStatsCollectPerQueuePerState(t *testing.T) {
	_, client, c := newQueueEnv(t)
	const qCrit, qDef = "qstats_critical", "qstats_default"

	// critical：2 pending + 1 scheduled；default：1 pending。
	for i := 0; i < 2; i++ {
		if _, err := client.Enqueue(asynq.NewTask("t:noop", nil), asynq.Queue(qCrit)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if _, err := client.Enqueue(asynq.NewTask("t:noop", nil), asynq.Queue(qCrit), asynq.ProcessIn(time.Hour)); err != nil {
		t.Fatalf("enqueue scheduled: %v", err)
	}
	if _, err := client.Enqueue(asynq.NewTask("t:noop", nil), asynq.Queue(qDef)); err != nil {
		t.Fatalf("enqueue default: %v", err)
	}

	if err := c.collect(); err != nil {
		t.Fatalf("collect: %v", err)
	}

	cases := []struct {
		queue, state string
		want         float64
	}{
		{qCrit, "pending", 2},
		{qCrit, "scheduled", 1},
		{qCrit, "active", 0},
		{qCrit, "retry", 0},
		{qCrit, "archived", 0},
		{qDef, "pending", 1},
	}
	for _, tc := range cases {
		if got := gaugeVal(t, tc.queue, tc.state); got != tc.want {
			t.Errorf("vigil_queue_tasks{queue=%q,state=%q} = %v, want %v", tc.queue, tc.state, got, tc.want)
		}
	}
}

// TestQueueStatsCollectArchived 死信可见性：任务归档（=重试耗尽的最终失败形态）后
// archived gauge 反映其数量——这是本采集器要补的核心盲区。
func TestQueueStatsCollectArchived(t *testing.T) {
	_, client, c := newQueueEnv(t)
	const q = "qstats_archived"

	info, err := client.Enqueue(asynq.NewTask("t:doom", nil), asynq.Queue(q))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// 用 Inspector 把 pending 任务归档，模拟重试耗尽进死信。
	if err := c.insp.ArchiveTask(q, info.ID); err != nil {
		t.Fatalf("archive task: %v", err)
	}

	if err := c.collect(); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := gaugeVal(t, q, "archived"); got != 1 {
		t.Errorf("archived gauge = %v, want 1", got)
	}
	if got := gaugeVal(t, q, "pending"); got != 0 {
		t.Errorf("pending gauge after archive = %v, want 0", got)
	}
}

// TestQueueStatsCollectErrorKeepsLastValues 采集失败（Redis 挂）时：错误计数递增，
// gauge 保留上次采集值——清零会被外部监控误读为「积压消失」，陈旧值 + 错误计数才诚实。
func TestQueueStatsCollectErrorKeepsLastValues(t *testing.T) {
	mr, client, c := newQueueEnv(t)
	const q = "qstats_stale"

	if _, err := client.Enqueue(asynq.NewTask("t:noop", nil), asynq.Queue(q)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	c.collectOnce() // 成功采集：pending=1
	if got := gaugeVal(t, q, "pending"); got != 1 {
		t.Fatalf("precondition pending = %v, want 1", got)
	}

	errBefore := testutil.ToFloat64(QueueStatsCollectErrors)
	mr.Close() // Redis 故障
	c.collectOnce()
	if got := testutil.ToFloat64(QueueStatsCollectErrors); got-errBefore != 1 {
		t.Errorf("collect errors counter should +1, before=%v after=%v", errBefore, got)
	}
	if got := gaugeVal(t, q, "pending"); got != 1 {
		t.Errorf("gauge must hold last value on collect failure, got %v want 1", got)
	}
}

// TestNewQueueStatsCollectorDegrade nil Inspector 返回 nil（wire 侧据此降级）；
// 非法 interval 回退默认 15s。
func TestNewQueueStatsCollectorDegrade(t *testing.T) {
	if NewQueueStatsCollector(nil, time.Second, nil) != nil {
		t.Error("nil inspector should yield nil collector")
	}
	mr := miniredis.RunT(t)
	insp := asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()})
	t.Cleanup(func() { _ = insp.Close() })
	c := NewQueueStatsCollector(insp, 0, nil)
	if c == nil || c.Interval() != 15*time.Second {
		t.Errorf("interval<=0 should fall back to 15s, got %v", c.Interval())
	}
}
