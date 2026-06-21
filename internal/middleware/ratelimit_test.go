package middleware

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newRedisTestClient 启动 miniredis 并返回连接它的 client（测试隔离）。
func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// mustLPush 往队列塞任务，失败即 fatal（测试前置数据，失败应中断）。
//
//nolint:revive // 测试 helper 惯例：*testing.T 必须为首参，与 context-first 规则冲突
func mustLPush(t *testing.T, ctx context.Context, rc *redis.Client, key, val string) {
	t.Helper()
	if err := rc.LPush(ctx, key, val).Err(); err != nil {
		t.Fatalf("lpush %s: %v", key, err)
	}
}

// === 降级路径（无 redis）===

func TestLimiter_NilRedisAllowsAll(t *testing.T) {
	var l *Limiter
	allowed, err := l.Allow(context.Background(), "k", 100)
	if err != nil || !allowed {
		t.Errorf("nil limiter: allowed=%v err=%v, want true/nil", allowed, err)
	}
}

func TestLimiter_ZeroLimitAllowsAll(t *testing.T) {
	l := NewLimiter(&redis.Client{})
	allowed, err := l.Allow(context.Background(), "k", 0)
	if err != nil || !allowed {
		t.Errorf("zero limit: allowed=%v err=%v, want true/nil", allowed, err)
	}
}

func TestLimiter_NilRedisNotAvailable(t *testing.T) {
	var l *Limiter
	if l.Available() {
		t.Error("nil limiter Available()=true, want false")
	}
	if NewLimiter(nil).Available() {
		t.Error("nil-redis limiter Available()=true, want false")
	}
}

// === 真实限流行为（miniredis）===

func TestLimiter_AllowsUnderLimit(t *testing.T) {
	l := NewLimiter(newRedisTestClient(t))
	ctx := context.Background()
	// limit=5，前 5 次应放行
	for i := 0; i < 5; i++ {
		allowed, err := l.Allow(ctx, "integration:1", 5)
		if err != nil || !allowed {
			t.Errorf("request %d: allowed=%v err=%v, want true", i+1, allowed, err)
		}
	}
}

func TestLimiter_BlocksOverLimit(t *testing.T) {
	l := NewLimiter(newRedisTestClient(t))
	ctx := context.Background()
	// limit=3，前 3 次放行，第 4 次拒绝
	for i := 0; i < 3; i++ {
		allowed, _ := l.Allow(ctx, "ip:1.2.3.4", 3)
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
	allowed, _ := l.Allow(ctx, "ip:1.2.3.4", 3)
	if allowed {
		t.Error("4th request should be blocked (over limit)")
	}
}

func TestLimiter_DifferentKeysIndependent(t *testing.T) {
	// 不同 key 的限流互不影响
	l := NewLimiter(newRedisTestClient(t))
	ctx := context.Background()
	// key A 用尽配额
	for i := 0; i < 2; i++ {
		_, _ = l.Allow(ctx, "a", 2)
	}
	// key B 仍可用
	allowed, _ := l.Allow(ctx, "b", 2)
	if !allowed {
		t.Error("key b blocked by key a's usage, want independent")
	}
}

func TestLimiter_ConnectionFailureDegradesToAllow(t *testing.T) {
	// 连不上的 redis（无效地址）应降级放行（可用性优先）。
	// 用 miniredis 启动后立即 Close 模拟"连不上"。
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close() // 关闭后 client 操作必失败

	l := NewLimiter(rc)
	allowed, err := l.Allow(context.Background(), "k", 1)
	if err != nil {
		t.Errorf("connection failure should not return error, got %v", err)
	}
	if !allowed {
		t.Error("connection failure should degrade to allow, got blocked")
	}
}

// === 背压检查器 ===

func TestBackpressure_NilOrZeroNotAvailable(t *testing.T) {
	var b *BackpressureChecker
	if b.Available() {
		t.Error("nil backpressure Available()=true")
	}
	if NewBackpressureChecker(nil, 1000).Available() {
		t.Error("nil-redis backpressure Available()=true")
	}
	if NewBackpressureChecker(&redis.Client{}, 0).Available() {
		t.Error("zero-depth backpressure Available()=true")
	}
}

func TestBackpressure_NotOverloadedUnderThreshold(t *testing.T) {
	rc := newRedisTestClient(t)
	ctx := context.Background()
	// 往 default 队列塞 5 个任务，阈值 10 → 未过载
	for i := 0; i < 5; i++ {
		mustLPush(t, ctx, rc, "asynq:default", "task")
	}
	b := NewBackpressureChecker(rc, 10)
	if b.IsOverloaded(ctx) {
		t.Error("5 tasks with threshold 10 should not be overloaded")
	}
}

func TestBackpressure_OverloadedOverThreshold(t *testing.T) {
	rc := newRedisTestClient(t)
	ctx := context.Background()
	// 塞 15 个任务，阈值 10 → 过载
	for i := 0; i < 15; i++ {
		mustLPush(t, ctx, rc, "asynq:critical", "task")
	}
	b := NewBackpressureChecker(rc, 10)
	if !b.IsOverloaded(ctx) {
		t.Error("15 tasks with threshold 10 should be overloaded")
	}
}

func TestBackpressure_SumsAcrossQueues(t *testing.T) {
	// critical=4 + default=4 + low=4 = 12 > 阈值 10
	rc := newRedisTestClient(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		mustLPush(t, ctx, rc, "asynq:critical", "t")
		mustLPush(t, ctx, rc, "asynq:default", "t")
		mustLPush(t, ctx, rc, "asynq:low", "t")
	}
	b := NewBackpressureChecker(rc, 10)
	if !b.IsOverloaded(ctx) {
		t.Error("12 tasks across queues (threshold 10) should be overloaded")
	}
}
