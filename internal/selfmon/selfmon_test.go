// selfmon_test.go 自监控引擎逻辑测试（不依赖真实基础设施，用 fake 注入）。
//
// 覆盖：队列深度阈值判定、失败率计算（样本不足不触发 / 超阈触发）、冷却生效（Cooldown
// 内不重发）、诚实边界（notifier/admin 缺失不 panic 且不假发）、队列探测连续失败红线
// （连续 N 次触发 / 成功重置 / 阈值关闭保持只 warn）。
package selfmon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kevin/vigil/internal/notification"
)

// fakeQueue 可控队列深度来源。
type fakeQueue struct {
	depth int
	err   error
}

func (f fakeQueue) Depth(context.Context) (int, error) { return f.depth, f.err }

// fakeRate 可控失败率来源。
type fakeRate struct {
	failed, total int
	err           error
}

func (f fakeRate) Rate(context.Context, time.Duration) (int, int, error) {
	return f.failed, f.total, f.err
}

// fakeNotifier 记录 Alert 调用（断言是否发、发了几次、走哪些通道）。
type fakeNotifier struct {
	mu       sync.Mutex
	calls    int
	kinds    []string // 每次 Alert 的 title（含 kind 语义）
	channels []string // 最近一次 channels
	err      error
}

func (f *fakeNotifier) Alert(_ context.Context, _ []notification.Target, title, _ string, channels []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.kinds = append(f.kinds, title)
	f.channels = channels
	return f.err
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeAdmins 固定返回一个收件人。
type fakeAdmins struct {
	targets []notification.Target
	err     error
}

func (f fakeAdmins) Resolve(context.Context) ([]notification.Target, error) {
	return f.targets, f.err
}

func oneAdmin() fakeAdmins {
	return fakeAdmins{targets: []notification.Target{{UserID: 1, Name: "admin", Source: "user"}}}
}

func baseCfg() Config {
	return Config{
		CheckInterval:              time.Minute,
		QueueDepthThreshold:        10000,
		FailureRateThreshold:       0.5,
		FailureRateWindow:          15 * time.Minute,
		FailureRateMinSample:       20,
		Cooldown:                   30 * time.Minute,
		AlertChannels:              []string{"webhook", "email"},
		QueueProbeFailureThreshold: 3, // 与生产默认一致（EffectiveQueueProbeFailureThreshold）
	}
}

// TestQueueDepthBelowThreshold 未超阈值不告警。
func TestQueueDepthBelowThreshold(t *testing.T) {
	notif := &fakeNotifier{}
	e := NewEngine(baseCfg(), fakeQueue{depth: 9999}, fakeRate{}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("expected no alert below threshold, got %d", notif.count())
	}
}

// TestQueueDepthOverThreshold 超阈值触发一次告警，且走独立通道（不含 im）。
func TestQueueDepthOverThreshold(t *testing.T) {
	notif := &fakeNotifier{}
	e := NewEngine(baseCfg(), fakeQueue{depth: 10001}, fakeRate{}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 1 {
		t.Fatalf("expected 1 alert over threshold, got %d", notif.count())
	}
	for _, ch := range notif.channels {
		if ch == "im" {
			t.Fatalf("self-monitor alert must not use im channel; got %v", notif.channels)
		}
	}
}

// TestQueueProbeErrorNoAlert 探测失败（Inspector 抖动）不误报积压。
func TestQueueProbeErrorNoAlert(t *testing.T) {
	notif := &fakeNotifier{}
	e := NewEngine(baseCfg(), fakeQueue{err: errors.New("redis down")}, fakeRate{}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("probe error must not alert, got %d", notif.count())
	}
}

// TestFailureRateInsufficientSample 样本不足（total<MinSample）不触发，即便 100% 失败。
func TestFailureRateInsufficientSample(t *testing.T) {
	notif := &fakeNotifier{}
	// 5 条全失败 = 100% 失败率，但 total<20（MinSample），不判。
	e := NewEngine(baseCfg(), fakeQueue{depth: 0}, fakeRate{failed: 5, total: 5}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("insufficient sample must not alert, got %d", notif.count())
	}
}

// TestFailureRateOverThreshold 样本足够且超阈：触发。
func TestFailureRateOverThreshold(t *testing.T) {
	notif := &fakeNotifier{}
	// 30 条中 20 失败 = 66.7% > 50%，样本 30>=20：触发。
	e := NewEngine(baseCfg(), fakeQueue{depth: 0}, fakeRate{failed: 20, total: 30}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 1 {
		t.Fatalf("over-threshold with enough sample must alert, got %d", notif.count())
	}
}

// TestFailureRateAtThresholdNoAlert 恰等于阈值不触发（严格大于才触发）。
func TestFailureRateAtThresholdNoAlert(t *testing.T) {
	notif := &fakeNotifier{}
	// 40 条中 20 失败 = 恰 50% = 阈值，不触发（rate<=threshold）。
	e := NewEngine(baseCfg(), fakeQueue{depth: 0}, fakeRate{failed: 20, total: 40}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("rate at threshold must not alert, got %d", notif.count())
	}
}

// TestCooldownSuppressesRepeat 冷却内同 kind 不重发；冷却过后可再发。
func TestCooldownSuppressesRepeat(t *testing.T) {
	notif := &fakeNotifier{}
	e := NewEngine(baseCfg(), fakeQueue{depth: 20000}, fakeRate{}, notif, oneAdmin(), nil)

	// 用可控时钟：第一次在 t0，第二次仍在冷却内（+10m<30m），第三次冷却已过（+40m）。
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := base
	e.now = func() time.Time { return cur }

	e.Check(t.Context()) // t0：应发
	if notif.count() != 1 {
		t.Fatalf("first check should alert once, got %d", notif.count())
	}

	cur = base.Add(10 * time.Minute)
	e.Check(t.Context()) // +10m：冷却内，不发
	if notif.count() != 1 {
		t.Fatalf("within cooldown should not re-alert, got %d", notif.count())
	}

	cur = base.Add(40 * time.Minute)
	e.Check(t.Context()) // +40m：冷却过，应再发
	if notif.count() != 2 {
		t.Fatalf("after cooldown should re-alert, got %d", notif.count())
	}
}

// TestCooldownPerKindIndependent 队列与失败率冷却互相独立（不同 kind 各自计冷却）。
func TestCooldownPerKindIndependent(t *testing.T) {
	notif := &fakeNotifier{}
	// 同一轮两项都超阈：应各发一条（2 次），互不因对方占冷却而漏发。
	e := NewEngine(baseCfg(), fakeQueue{depth: 20000}, fakeRate{failed: 25, total: 30}, notif, oneAdmin(), nil)
	e.Check(t.Context())
	if notif.count() != 2 {
		t.Fatalf("queue and failure-rate alerts are independent; expected 2, got %d", notif.count())
	}
}

// TestMissingNotifierNoPanic 诚实边界：notifier/admin 缺失时不 panic、不假发。
func TestMissingNotifierNoPanic(t *testing.T) {
	e := NewEngine(baseCfg(), fakeQueue{depth: 20000}, fakeRate{}, nil, nil, nil)
	// 不应 panic；无 notifier 即无从发送。
	e.Check(t.Context())
}

// TestNoAdminNoAlert 无 org_admin 收件人时不发（解算为空）。
func TestNoAdminNoAlert(t *testing.T) {
	notif := &fakeNotifier{}
	e := NewEngine(baseCfg(), fakeQueue{depth: 20000}, fakeRate{}, notif, fakeAdmins{}, nil)
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("no admin should mean no alert, got %d", notif.count())
	}
}

// flakyQueue 可在测试中途切换成功/失败的队列来源（模拟 Redis 故障与恢复）。
type flakyQueue struct {
	mu    sync.Mutex
	depth int
	err   error
}

func (f *flakyQueue) set(depth int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.depth, f.err = depth, err
}

func (f *flakyQueue) Depth(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.depth, f.err
}

// TestQueueProbeConsecutiveFailuresFireRedLine 连续失败达到阈值（3）才触发红线告警；
// 之后持续失败由冷却抑制（不刷屏）。前 2 次失败只 warn 不告警（防抖动保留）。
func TestQueueProbeConsecutiveFailuresFireRedLine(t *testing.T) {
	notif := &fakeNotifier{}
	q := &flakyQueue{}
	q.set(0, errors.New("redis down"))
	e := NewEngine(baseCfg(), q, fakeRate{}, notif, oneAdmin(), nil)

	e.Check(t.Context()) // 第 1 次失败：只 warn
	e.Check(t.Context()) // 第 2 次失败：只 warn
	if notif.count() != 0 {
		t.Fatalf("below consecutive threshold must not alert, got %d", notif.count())
	}
	e.Check(t.Context()) // 第 3 次连续失败：红线触发
	if notif.count() != 1 {
		t.Fatalf("3rd consecutive probe failure must fire red-line alert, got %d", notif.count())
	}
	if notif.kinds[0] != "[自监控] 队列探测连续失败" {
		t.Fatalf("unexpected alert title: %q", notif.kinds[0])
	}
	e.Check(t.Context()) // 第 4 次：仍失败，但冷却内不重发
	if notif.count() != 1 {
		t.Fatalf("continued failures within cooldown must not re-alert, got %d", notif.count())
	}
}

// TestQueueProbeFailureResetOnSuccess 探测成功重置连续计数：失败必须「连续」达到阈值，
// 中间任何一次成功都从零重计（恢复解除语义 = 计数清零 + 告警自然停止）。
func TestQueueProbeFailureResetOnSuccess(t *testing.T) {
	notif := &fakeNotifier{}
	q := &flakyQueue{}
	e := NewEngine(baseCfg(), q, fakeRate{}, notif, oneAdmin(), nil)

	q.set(0, errors.New("redis down"))
	e.Check(t.Context()) // 失败 ×2
	e.Check(t.Context())
	q.set(100, nil)
	e.Check(t.Context()) // 成功：计数清零（深度 100 远低于积压阈值，不触发积压告警）
	q.set(0, errors.New("redis down"))
	e.Check(t.Context()) // 再失败 ×2：非连续 3 次
	e.Check(t.Context())
	if notif.count() != 0 {
		t.Fatalf("non-consecutive failures must not alert, got %d", notif.count())
	}
	e.Check(t.Context()) // 第 3 次连续失败：触发
	if notif.count() != 1 {
		t.Fatalf("3 consecutive failures after reset must alert, got %d", notif.count())
	}
}

// TestQueueProbeRedLineDisabled 阈值 <=0 时红线关闭：持续失败也维持原「只 warn」行为。
func TestQueueProbeRedLineDisabled(t *testing.T) {
	notif := &fakeNotifier{}
	cfg := baseCfg()
	cfg.QueueProbeFailureThreshold = 0
	e := NewEngine(cfg, fakeQueue{err: errors.New("redis down")}, fakeRate{}, notif, oneAdmin(), nil)
	for i := 0; i < 5; i++ {
		e.Check(t.Context())
	}
	if notif.count() != 0 {
		t.Fatalf("disabled red line must never alert, got %d", notif.count())
	}
}

// TestQueueProbeReAlertAfterRecoveryAndCooldown 恢复后再次连续失败：冷却已过则再次告警
// （红线可重复进入，与现有 kind 的冷却语义一致）。
func TestQueueProbeReAlertAfterRecoveryAndCooldown(t *testing.T) {
	notif := &fakeNotifier{}
	q := &flakyQueue{}
	cfg := baseCfg()
	cfg.QueueProbeFailureThreshold = 2
	e := NewEngine(cfg, q, fakeRate{}, notif, oneAdmin(), nil)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := base
	e.now = func() time.Time { return cur }

	q.set(0, errors.New("redis down"))
	e.Check(t.Context()) // 失败 1
	e.Check(t.Context()) // 失败 2：告警 #1
	if notif.count() != 1 {
		t.Fatalf("first streak should alert once, got %d", notif.count())
	}

	cur = base.Add(40 * time.Minute) // 冷却（30m）已过
	q.set(100, nil)
	e.Check(t.Context()) // 恢复：计数清零
	q.set(0, errors.New("redis down again"))
	e.Check(t.Context()) // 失败 1：不足阈值，不告警
	if notif.count() != 1 {
		t.Fatalf("single failure after recovery must not alert, got %d", notif.count())
	}
	e.Check(t.Context()) // 失败 2：新一轮连续达到阈值且冷却已过 → 告警 #2
	if notif.count() != 2 {
		t.Fatalf("new streak after cooldown should re-alert, got %d", notif.count())
	}
}

// TestAlertDeliveryFailureStillCoolsDown 发送失败也进冷却（避免每 interval 重试发不出去的告警）。
func TestAlertDeliveryFailureStillCoolsDown(t *testing.T) {
	notif := &fakeNotifier{err: errors.New("channel down")}
	e := NewEngine(baseCfg(), fakeQueue{depth: 20000}, fakeRate{}, notif, oneAdmin(), nil)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := base
	e.now = func() time.Time { return cur }

	e.Check(t.Context()) // 尝试发送（失败），但仍记冷却
	if notif.count() != 1 {
		t.Fatalf("first attempt should call notifier once, got %d", notif.count())
	}
	cur = base.Add(5 * time.Minute)
	e.Check(t.Context()) // 冷却内：不再重试
	if notif.count() != 1 {
		t.Fatalf("failed delivery should still cool down (no immediate retry), got %d", notif.count())
	}
}
