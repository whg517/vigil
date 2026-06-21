package escalation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	_ "github.com/mattn/go-sqlite3"
)

// TestEscalationTaskID 验证任务 ID 生成的稳定性与唯一性（用于幂等与取消）。
func TestEscalationTaskID(t *testing.T) {
	cases := []struct {
		incID, levelIdx, repeat int
		want                    string
	}{
		{42, 0, 0, "esc:42:0:0"},
		{42, 1, 0, "esc:42:1:0"},
		{42, 0, 2, "esc:42:0:2"},
		{1, 3, 1, "esc:1:3:1"},
	}
	for _, c := range cases {
		if got := escalationTaskID(c.incID, c.levelIdx, c.repeat); got != c.want {
			t.Errorf("escalationTaskID(%d,%d,%d): got %q, want %q", c.incID, c.levelIdx, c.repeat, got, c.want)
		}
	}
	// 唯一性：不同 (inc,level,repeat) 不撞
	seen := map[string]bool{}
	for inc := 1; inc <= 5; inc++ {
		for lv := 0; lv < 3; lv++ {
			for rp := 0; rp < 3; rp++ {
				id := escalationTaskID(inc, lv, rp)
				if seen[id] {
					t.Errorf("task id collision: %s", id)
				}
				seen[id] = true
			}
		}
	}
}

// TestNewEngine_NilDeps 验证构造函数对 nil 依赖的容错（测试场景）。
func TestNewEngine_NilDeps(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}
	// inspector 在 redisOpt=nil 时应返回 nil
	if ins := e.inspector(); ins != nil {
		t.Errorf("inspector should be nil when redisOpt is nil")
	}
}

// TestCancelOnAck_NoRedis 验证无 Redis 时 CancelOnAck 不 panic（状态守卫兜底）。
func TestCancelOnAck_NoRedis(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:esc_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })

	e := NewEngine(c, nil, nil, nil, nil)
	// 无 Redis（inspector=nil），应安全返回 nil
	if err := e.CancelOnAck(context.TODO(), 1, nil, 0); err != nil {
		t.Errorf("CancelOnAck with no redis: %v", err)
	}
}

// —— 集成测试辅助 ——

// fakeNotifier 记录 NotifyEscalation 调用，用于断言升级是否触发通知。
type fakeNotifier struct {
	calls []fakeNotifyCall
}

type fakeNotifyCall struct {
	IncID  int
	Level  int
	Target []NotifyTarget
}

func (f *fakeNotifier) NotifyEscalation(_ context.Context, inc *ent.Incident, level int, targets []NotifyTarget) error {
	f.calls = append(f.calls, fakeNotifyCall{IncID: inc.ID, Level: level, Target: targets})
	return nil
}

// escTestEnv 集成测试环境：sqlite + miniredis + asynq queue + engine。
type escTestEnv struct {
	client   *ent.Client
	mr       *miniredis.Miniredis
	q        *queue.Queue
	engine   *Engine
	notifier *fakeNotifier
}

// newEscEnv 构造升级引擎测试环境。
// seedPolicy 的 levels 决定升级链结构。
func newEscEnv(t *testing.T, levels []schema.EscalationLevel, repeatTimes int) *escTestEnv {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:esc_int_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })

	mr := miniredis.RunT(t)
	redisOpt := asynq.RedisClientOpt{Addr: mr.Addr()}
	// 用真实 config 喂给 queue.New（仅 Redis 部分有意义）
	cfg := &config.Config{}
	cfg.Redis.Addr = mr.Addr()
	q := queue.New(cfg)
	t.Cleanup(func() { _ = q.Close() })

	notifier := &fakeNotifier{}
	engine := NewEngine(c, q, nil, notifier, &redisOpt)
	engine.SetRecorder(timeline.NewRecorder(c))

	// seed team + policy + incident
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("T").SetSlug("t").Save(ctx)
	policy, _ := c.EscalationPolicy.Create().
		SetName("p").
		SetRepeatTimes(repeatTimes).
		SetLevels(levels).
		SetTeamID(team.ID).
		Save(ctx)
	inc, _ := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("test").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).
		SetEscalationPolicyID(policy.ID).
		Save(ctx)
	_ = inc

	return &escTestEnv{client: c, mr: mr, q: q, engine: engine, notifier: notifier}
}

// incID 取 seeded incident 的 id。
func (e *escTestEnv) incID(t *testing.T) int {
	t.Helper()
	inc, err := e.client.Incident.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	return inc.ID
}

// runEscTask 构造并执行一个升级任务（绕过队列直接调 HandleTask，便于同步断言）。
func (e *escTestEnv) runEscTask(t *testing.T, incID, levelIdx, repeatSeq int) {
	t.Helper()
	payload, _ := json.Marshal(escalationTask{IncidentID: incID, LevelIdx: levelIdx, RepeatSeq: repeatSeq})
	if err := e.engine.HandleTask(context.Background(), asynq.NewTask(TaskEscalation, payload)); err != nil {
		t.Fatalf("HandleTask(%d,%d,%d): %v", incID, levelIdx, repeatSeq, err)
	}
}

// TestHandleTask_StateGuard_Acked 已 ack 的 incident 触发升级任务时不动作（状态守卫）。
func TestHandleTask_StateGuard_Acked(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	// 把 incident 置为 acked
	if err := env.client.Incident.UpdateOneID(id).SetStatus(incident.StatusAcked).Exec(context.Background()); err != nil {
		t.Fatalf("set acked: %v", err)
	}

	env.runEscTask(t, id, 0, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("acked incident should not trigger notification, got %d calls", len(env.notifier.calls))
	}
}

// TestHandleTask_StateGuard_Resolved 已 resolved 的 incident 不动作。
func TestHandleTask_StateGuard_Resolved(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	now := time.Now()
	if err := env.client.Incident.UpdateOneID(id).
		SetStatus(incident.StatusResolved).SetResolvedAt(now).Exec(context.Background()); err != nil {
		t.Fatalf("set resolved: %v", err)
	}

	env.runEscTask(t, id, 0, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("resolved incident should not trigger notification, got %d calls", len(env.notifier.calls))
	}
}

// TestHandleTask_NormalPath triggered incident 触发 level[0]：
// 推进状态到 escalated、current_level=1、记时间线、调 notifier。
func TestHandleTask_NormalPath(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	env.runEscTask(t, id, 0, 0)

	// incident 状态推进
	inc, _ := env.client.Incident.Get(context.Background(), id)
	if inc.Status != incident.StatusEscalated {
		t.Errorf("status: got %s, want escalated", inc.Status)
	}
	if inc.CurrentLevel != 1 {
		t.Errorf("current_level: got %d, want 1", inc.CurrentLevel)
	}
	if inc.EscalatedCount != 1 {
		t.Errorf("escalated_count: got %d, want 1", inc.EscalatedCount)
	}
	// notifier 被调用（level 0）
	if len(env.notifier.calls) != 1 || env.notifier.calls[0].Level != 0 {
		t.Errorf("notifier calls: got %+v, want 1 call at level 0", env.notifier.calls)
	}
	// 时间线有 escalated 记录
	items, _ := env.client.Incident.Query().Where(incident.IDEQ(id)).QueryTimeline().All(context.Background())
	if len(items) == 0 {
		t.Error("expected timeline item for escalation")
	}
}

// TestHandleTask_LevelIdxOutOfBounds levelIdx 超过 policy levels 时不动作（幂等）。
func TestHandleTask_LevelIdxOutOfBounds(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	// levelIdx=5 超出唯一 level
	env.runEscTask(t, id, 5, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("out-of-bounds level should not notify, got %d", len(env.notifier.calls))
	}
}

// TestTriggerLevelNow 入队即时升级任务到真实 asynq（miniredis），
// 验证任务确实被 enqueue（Inspector 能查到）。
func TestTriggerLevelNow(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	if err := env.engine.TriggerLevelNow(context.Background(), id, 0); err != nil {
		t.Fatalf("TriggerLevelNow: %v", err)
	}

	// 用 Inspector 查 critical 队列应有 1 个待处理任务
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	tasks, err := inspector.ListPendingTasks("critical", asynq.PageSize(10))
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("pending tasks: got %d, want 1", len(tasks))
	}
	if tasks[0].Type != TaskEscalation {
		t.Errorf("task type: got %s, want %s", tasks[0].Type, TaskEscalation)
	}
}

// TestCancelOnAck_RealInspector 有真实 Inspector 时，
// StartEscalation 入队的任务被 CancelOnAck 删除（状态守卫之外的主动取消）。
func TestCancelOnAck_RealInspector(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 1)
	id := env.incID(t)

	// 启动升级链（入队延迟任务）
	if err := env.engine.StartEscalation(context.Background(), id, levels); err != nil {
		t.Fatalf("StartEscalation: %v", err)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	// delay>0 的任务在 scheduled 队列（未到触发时间），pending 为空
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(20))
	if len(scheduled) == 0 {
		t.Fatal("expected scheduled tasks after StartEscalation")
	}

	// CancelOnAck 删除所有待触发任务
	if err := env.engine.CancelOnAck(context.Background(), id, levels, 1); err != nil {
		t.Fatalf("CancelOnAck: %v", err)
	}
	after, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(20))
	if len(after) != 0 {
		t.Errorf("scheduled after cancel: got %d, want 0", len(after))
	}
}

// TestResolveTargets_UserTarget user 类型 target 解析成 NotifyTarget（去重）。
func TestResolveTargets_UserTarget(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	ctx := context.Background()
	// 建两个用户
	u1, _ := env.client.User.Create().SetUsername("u1").SetEmail("u1@x").Save(ctx)
	u2, _ := env.client.User.Create().SetUsername("u2").SetEmail("u2@x").Save(ctx)

	targets := []schema.Target{
		{Type: "user", TargetID: itoa(u1.ID)},
		{Type: "user", TargetID: itoa(u2.ID)},
		{Type: "user", TargetID: itoa(u1.ID)}, // 重复，应去重
		{Type: "user", TargetID: "99999"},     // 不存在，跳过（记 warn）
	}
	got, err := env.engine.resolveTargets(ctx, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("resolved count: got %d, want 2 (dedup + skip missing)", len(got))
	}
	// 不存在的 target 不应导致结果包含错误项
	for _, nt := range got {
		if nt.UserID == 99999 {
			t.Error("missing user should not appear in results")
		}
	}
}

// TestResolveTargets_TeamTarget team 类型 target 标记 source=team（通知引擎处理）。
func TestResolveTargets_TeamTarget(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	got, err := env.engine.resolveTargets(context.Background(), []schema.Target{
		{Type: "team", TargetID: "5"},
	})
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 1 || got[0].Source != "team" {
		t.Errorf("team target: got %+v, want 1 with source=team", got)
	}
}

// itoa 整数转字符串（避免引入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
