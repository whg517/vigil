// delivery_task_test.go 通知投递 Asynq 化（ADR-0017 修订）行为测试：
//   - 入队路径：NotifyEscalation 落 pending 行 + 入队任务（TaskID=notif:{id}，critical 队列）
//   - worker 幂等守卫：行已终态时重投跳过（不重复送达）
//   - 瞬时失败：Handle 返回 error（交给 asynq 退避重试），行保持 pending
//   - 最终失败：行落 failed + allFailedHook 只在最后一次触发
//   - 聚合路径（FlushAll）产生的通知同样走任务投递
//   - 入队失败回退同步直投（绝不丢通知）
//   - NotifyUnrouted 刻意保持同步（自监控/兜底告警不依赖队列）
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	entnotification "github.com/kevin/vigil/ent/notification"
	"github.com/kevin/vigil/internal/escalation"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// fakeEnqueuer 捕获入队调用的 TaskEnqueuer 假实现，可注入错误模拟 Redis 不可用。
type fakeEnqueuer struct {
	tasks []*asynq.Task
	opts  [][]asynq.Option
	err   error
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, t *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.tasks = append(f.tasks, t)
	f.opts = append(f.opts, opts)
	return &asynq.TaskInfo{ID: "fake"}, nil
}

// optValue 从入队选项里提取指定类型的值（断言 TaskID/Queue/MaxRetry 用）。
func optValue(opts []asynq.Option, typ asynq.OptionType) any {
	for _, o := range opts {
		if o.Type() == typ {
			return o.Value()
		}
	}
	return nil
}

// overrideRetryInfo 覆盖 asynq 重试上下文读取（getRetryCount/getMaxRetry 包级变量），
// 使单测能直调 Handle 走「非最后一次 / 最后一次」两条路径。
func overrideRetryInfo(t *testing.T, retried, maxRetry int) {
	t.Helper()
	origCount, origMax := getRetryCount, getMaxRetry
	getRetryCount = func(context.Context) (int, bool) { return retried, true }
	getMaxRetry = func(context.Context) (int, bool) { return maxRetry, true }
	t.Cleanup(func() { getRetryCount, getMaxRetry = origCount, origMax })
}

// mkDeliveryPayload 构造一条投递任务（先落 pending 行，返回 task + 行 ID）。
func mkDeliveryPayload(t *testing.T, c *ent.Client, incID int, chans []string, severity string) (*asynq.Task, int) {
	t.Helper()
	store := NewDeliveryStore(c)
	id, err := store.CreatePending(context.Background(), DeliveryRecord{
		IncidentID: incID, UserID: 1, Channel: chans[0], Target: "user:1",
		Status: StatusPending, Reason: "queued for delivery", Severity: severity,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	payload, _ := json.Marshal(deliveryTask{
		NotificationID: id, IncidentID: incID,
		Target: Target{UserID: 1, Name: "u", Source: "user"},
		Title:  "t", Summary: "s", Channels: chans, Severity: severity,
	})
	return asynq.NewTask(TaskDeliver, payload), id
}

// rowStatus 读某 Notification 行状态。
func rowStatus(t *testing.T, c *ent.Client, id int) entnotification.Status {
	t.Helper()
	row, err := c.Notification.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get notification %d: %v", id, err)
	}
	return row.Status
}

// TestDeliveryWorker_SuccessSetsSent 送达成功 → 行置 sent（记实际通道），任务完成。
func TestDeliveryWorker_SuccessSetsSent(t *testing.T) {
	c := newDispatchClient(t)
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetAsyncDelivery(&fakeEnqueuer{}, NewDeliveryStore(c))
	overrideRetryInfo(t, 0, deliverMaxRetry)

	task, id := mkDeliveryPayload(t, c, inc.ID, []string{"email"}, "critical")
	if err := NewDeliveryWorker(c, n).Handle(context.Background(), task); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if email.calls != 1 {
		t.Errorf("email calls: got %d, want 1", email.calls)
	}
	if st := rowStatus(t, c, id); st != entnotification.StatusSent {
		t.Errorf("row status: got %s, want sent", st)
	}
}

// TestDeliveryWorker_IdempotentGuard 行已终态（sent）时重投直接跳过——不重复送达。
func TestDeliveryWorker_IdempotentGuard(t *testing.T) {
	c := newDispatchClient(t)
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetAsyncDelivery(&fakeEnqueuer{}, NewDeliveryStore(c))
	overrideRetryInfo(t, 0, deliverMaxRetry)

	task, id := mkDeliveryPayload(t, c, inc.ID, []string{"email"}, "critical")
	w := NewDeliveryWorker(c, n)
	// 首投送达
	if err := w.Handle(context.Background(), task); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// at-least-once 重投：守卫应跳过，不再触达通道
	if err := w.Handle(context.Background(), task); err != nil {
		t.Fatalf("redelivered Handle: %v", err)
	}
	if email.calls != 1 {
		t.Errorf("email calls after redelivery: got %d, want 1 (guard must skip)", email.calls)
	}
	if st := rowStatus(t, c, id); st != entnotification.StatusSent {
		t.Errorf("row status: got %s, want sent", st)
	}
}

// TestDeliveryWorker_TransientFailureRetries 瞬时失败：Handle 返回 error（交 asynq 退避重试），
// 行保持 pending（reason 记最后错误），hook 不触发、不落 failed。
func TestDeliveryWorker_TransientFailureRetries(t *testing.T) {
	c := newDispatchClient(t)
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email", fail: true}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetAsyncDelivery(&fakeEnqueuer{}, NewDeliveryStore(c))
	hookFired := 0
	n.SetAllFailedHook(func(context.Context, *ent.Incident, Target, string, string) { hookFired++ })
	overrideRetryInfo(t, 0, deliverMaxRetry) // 第 1 次尝试，非最后一次

	task, id := mkDeliveryPayload(t, c, inc.ID, []string{"email"}, "critical")
	err := NewDeliveryWorker(c, n).Handle(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for transient failure (asynq retry driver), got nil")
	}
	if errors.Is(err, asynq.SkipRetry) {
		t.Errorf("transient failure must NOT skip retry: %v", err)
	}
	if st := rowStatus(t, c, id); st != entnotification.StatusPending {
		t.Errorf("row status: got %s, want pending (still retrying)", st)
	}
	row, _ := c.Notification.Get(context.Background(), id)
	if !strings.Contains(row.Reason, "boom") {
		t.Errorf("reason should carry last error, got %q", row.Reason)
	}
	if hookFired != 0 {
		t.Errorf("allFailedHook must not fire before final attempt, fired %d", hookFired)
	}
}

// TestDeliveryWorker_FinalFailure 最后一次重试仍失败：行落 failed + hook 只触发一次，
// Handle 返回 error（asynq 归档进死信）；再次重投被守卫跳过（hook 不会二次轰炸）。
func TestDeliveryWorker_FinalFailure(t *testing.T) {
	c := newDispatchClient(t)
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email", fail: true}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetAsyncDelivery(&fakeEnqueuer{}, NewDeliveryStore(c))
	hookFired := 0
	n.SetAllFailedHook(func(context.Context, *ent.Incident, Target, string, string) { hookFired++ })
	overrideRetryInfo(t, deliverMaxRetry, deliverMaxRetry) // 重试已耗尽

	task, id := mkDeliveryPayload(t, c, inc.ID, []string{"email"}, "critical")
	w := NewDeliveryWorker(c, n)
	if err := w.Handle(context.Background(), task); err == nil {
		t.Fatal("final failure should return error (task goes to archived dead letter)")
	}
	if st := rowStatus(t, c, id); st != entnotification.StatusFailed {
		t.Errorf("row status: got %s, want failed", st)
	}
	if hookFired != 1 {
		t.Errorf("allFailedHook: fired %d, want exactly 1 (only on final attempt)", hookFired)
	}
	// 极端重投（archived 后人工重放等）：守卫跳过，hook 不重复触发
	if err := w.Handle(context.Background(), task); err != nil {
		t.Fatalf("replay after failed: %v", err)
	}
	if hookFired != 1 {
		t.Errorf("allFailedHook after replay: fired %d, want 1", hookFired)
	}
}

// TestDeliveryWorker_NoChannelSkipsRetry 无任何可用通道（配置性失败）：重试无益，
// 直接落 failed + SkipRetry 归档，不做无意义的 5 轮退避。
func TestDeliveryWorker_NoChannelSkipsRetry(t *testing.T) {
	c := newDispatchClient(t)
	inc, _, _ := mkIncident(t, c, "critical")

	n := NewNotifier(NewRegistry(), []string{"email"}) // registry 为空：链上无通道
	n.SetAsyncDelivery(&fakeEnqueuer{}, NewDeliveryStore(c))
	overrideRetryInfo(t, 0, deliverMaxRetry)

	task, id := mkDeliveryPayload(t, c, inc.ID, []string{"email"}, "critical")
	err := NewDeliveryWorker(c, n).Handle(context.Background(), task)
	if err == nil || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("expected SkipRetry error for no-channel config failure, got %v", err)
	}
	if st := rowStatus(t, c, id); st != entnotification.StatusFailed {
		t.Errorf("row status: got %s, want failed", st)
	}
}

// TestNotifyEscalation_EnqueuesTask 入队路径（真实 asynq + miniredis）：
// critical 通知 → pending 行 + critical 队列任务，TaskID=notif:{行 ID}；通道不被同步触达。
func TestNotifyEscalation_EnqueuesTask(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	mr := miniredis.RunT(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetAsyncDelivery(client, NewDeliveryStore(c))

	if err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil); err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if email.calls != 0 {
		t.Errorf("channel must not be hit synchronously in async mode, calls=%d", email.calls)
	}
	// pending 行已先落库
	rows, err := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusPending)).All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows: got %d (err=%v), want 1", len(rows), err)
	}
	// critical 队列里有一个任务，TaskID=notif:{行 ID}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()})
	t.Cleanup(func() { _ = inspector.Close() })
	tasks, err := inspector.ListPendingTasks("critical", asynq.PageSize(10))
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("critical queue tasks: got %d, want 1", len(tasks))
	}
	if tasks[0].Type != TaskDeliver {
		t.Errorf("task type: got %s, want %s", tasks[0].Type, TaskDeliver)
	}
	if want := deliveryTaskID(rows[0].ID); tasks[0].ID != want {
		t.Errorf("task id: got %s, want %s", tasks[0].ID, want)
	}
	if tasks[0].MaxRetry != deliverMaxRetry {
		t.Errorf("max retry: got %d, want %d", tasks[0].MaxRetry, deliverMaxRetry)
	}
}

// TestNotifyEscalation_NonCriticalUsesDefaultQueue 非 critical 走 default 队列（沿用队列约定）。
func TestNotifyEscalation_NonCriticalUsesDefaultQueue(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")

	reg := NewRegistry()
	reg.Register(&stubChannel{name: "email"})
	n := NewNotifier(reg, []string{"email"})
	fe := &fakeEnqueuer{}
	n.SetAsyncDelivery(fe, NewDeliveryStore(c))
	// 不设 aggregator：warning 立即走 deliverChain → 入队

	if err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil); err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if len(fe.tasks) != 1 {
		t.Fatalf("enqueued tasks: got %d, want 1", len(fe.tasks))
	}
	if q := optValue(fe.opts[0], asynq.QueueOpt); q != "default" {
		t.Errorf("queue: got %v, want default", q)
	}
}

// TestNotifyEscalation_EnqueueFailureFallsBackSync 入队失败（Redis 不可用）：
// 回退同步直投（绝不丢通知），tracking 行按同步结果落终态、不留孤儿 pending。
func TestNotifyEscalation_EnqueueFailureFallsBackSync(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	fe := &fakeEnqueuer{err: errors.New("redis down")}
	n.SetAsyncDelivery(fe, NewDeliveryStore(c))

	if err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil); err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if email.calls != 1 {
		t.Errorf("sync fallback should deliver immediately, calls=%d", email.calls)
	}
	pending, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusPending)).Count(ctx)
	if pending != 0 {
		t.Errorf("no orphan pending row expected, got %d", pending)
	}
	sent, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusSent)).Count(ctx)
	if sent != 1 {
		t.Errorf("sent rows: got %d, want 1", sent)
	}
}

// TestFlushAggregated_EnqueuesTask 聚合路径：窗口到期 FlushAll 合并出的通知同样走任务投递。
func TestFlushAggregated_EnqueuesTask(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	fe := &fakeEnqueuer{}
	n.SetAsyncDelivery(fe, NewDeliveryStore(c))
	n.SetAggregator(NewAggregator(rc, 100*time.Millisecond))

	// warning 进聚合队列（不立即发送、不入队投递任务）
	if err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil); err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if len(fe.tasks) != 0 {
		t.Fatalf("aggregated notify should not enqueue delivery yet, got %d", len(fe.tasks))
	}
	// 窗口到期（:win 标记过期）→ FlushAll 合并发送 → 走任务投递
	mr.FastForward(200 * time.Millisecond)
	flushed, err := n.FlushAll(ctx)
	if err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("flushed targets: got %d, want 1", flushed)
	}
	if len(fe.tasks) != 1 {
		t.Fatalf("flush should enqueue 1 delivery task, got %d", len(fe.tasks))
	}
	if fe.tasks[0].Type() != TaskDeliver {
		t.Errorf("task type: got %s, want %s", fe.tasks[0].Type(), TaskDeliver)
	}
	if email.calls != 0 {
		t.Errorf("flush must not deliver synchronously in async mode, calls=%d", email.calls)
	}
	pending, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusPending)).Count(ctx)
	if pending != 1 {
		t.Errorf("pending tracking rows: got %d, want 1", pending)
	}
}

// TestNotifyUnrouted_StaysSync 自监控/兜底告警路径（NotifyUnrouted）刻意不走队列：
// 被监控的可能正是队列本身，兜底通知必须同步直投。
func TestNotifyUnrouted_StaysSync(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	fe := &fakeEnqueuer{}
	n.SetAsyncDelivery(fe, NewDeliveryStore(c))

	if err := n.NotifyUnrouted(ctx, []Target{{UserID: 1, Name: "admin", Source: "user"}}, "t", "s", nil); err != nil {
		t.Fatalf("NotifyUnrouted: %v", err)
	}
	if len(fe.tasks) != 0 {
		t.Errorf("unrouted must not enqueue, got %d tasks", len(fe.tasks))
	}
	if email.calls != 1 {
		t.Errorf("unrouted should deliver synchronously, calls=%d", email.calls)
	}
}
