package notification

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestAggregator_CriticalBypasses critical 立即发送，不入队。
// （critical 路径不依赖 Redis，便于单测）。
func TestAggregator_CriticalBypasses(t *testing.T) {
	a := NewAggregator(nil, 30*time.Second) // 无 Redis
	dec, err := a.Add(context.Background(), "user:1", "critical", AggregatedItem{IncidentID: 7, Title: "boom"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !dec.SendNow {
		t.Error("critical 应立即发送（SendNow=true）")
	}
}

// TestAggregator_NoRedisDegrades 无 Redis 时非 critical 也立即发送（降级保证送达）。
func TestAggregator_NoRedisDegrades(t *testing.T) {
	a := NewAggregator(nil, 30*time.Second)
	dec, err := a.Add(context.Background(), "user:1", "warning", AggregatedItem{IncidentID: 7})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !dec.SendNow {
		t.Error("无 Redis 时应降级立即发送")
	}
}

// TestAggregator_FlushNoRedis 无 Redis 时 Flush 返回 nil（无操作）。
func TestAggregator_FlushNoRedis(t *testing.T) {
	a := NewAggregator(nil, 30*time.Second)
	items, err := a.Flush(context.Background(), "user:1")
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if items != nil {
		t.Errorf("无 Redis Flush 应返回 nil，got %v", items)
	}
}

// TestAggregator_DefaultWindow window<=0 用默认 30s。
func TestAggregator_DefaultWindow(t *testing.T) {
	a := NewAggregator(nil, 0)
	if a.Window() != 30*time.Second {
		t.Errorf("默认窗口应为 30s，got %v", a.Window())
	}
	a2 := NewAggregator(nil, -5*time.Second)
	if a2.Window() != 30*time.Second {
		t.Errorf("负窗口应回退 30s，got %v", a2.Window())
	}
}

// —— QA 审计 C3：聚合死信修复 ——
// 用 miniredis 验证窗口过期后数据仍存活、PendingTargets 发现、Flush 取出并发送。

// TestAggregator_FlushAfterWindowRetainsData C3 核心：窗口过期后 Flush 能取到数据
// （旧实现 TTL=window 导致窗口到点数据被删，Flush 返回空 = 死信）。
func TestAggregator_FlushAfterWindowRetainsData(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()
	a := NewAggregator(rc, 30*time.Second)

	ctx := context.Background()
	// 入两条 warning
	_, err := a.Add(ctx, "user:1", "warning", AggregatedItem{IncidentID: 1, Title: "w1"})
	if err != nil {
		t.Fatalf("Add w1: %v", err)
	}
	_, err = a.Add(ctx, "user:1", "warning", AggregatedItem{IncidentID: 2, Title: "w2"})
	if err != nil {
		t.Fatalf("Add w2: %v", err)
	}

	// 窗口未到：Flush 应返回 nil（等待）
	items, err := a.Flush(ctx, "user:1")
	if err != nil {
		t.Fatalf("Flush before window: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("窗口未到 Flush 应返回空，got %d items", len(items))
	}

	// 推进时间过窗口
	mr.FastForward(31 * time.Second)

	// C3 关键断言：窗口到点后 Flush 必须能取到 2 条（旧实现此处取到 0 = 死信）
	items, err = a.Flush(ctx, "user:1")
	if err != nil {
		t.Fatalf("Flush after window: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("C3 回归：窗口到点后 Flush 应取到 2 条聚合通知，got %d（旧实现此处为 0 = 死信）", len(items))
	}
}

// TestAggregator_PendingTargets 发现待 flush 的 target（供 FlushAll 周期驱动）。
func TestAggregator_PendingTargets(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()
	a := NewAggregator(rc, 30*time.Second)
	ctx := context.Background()

	_, _ = a.Add(ctx, "user:1", "warning", AggregatedItem{IncidentID: 1})
	_, _ = a.Add(ctx, "user:2", "warning", AggregatedItem{IncidentID: 2})

	targets, err := a.PendingTargets(ctx)
	if err != nil {
		t.Fatalf("PendingTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 pending targets, got %d: %v", len(targets), targets)
	}
	// 不应包含 :win 标记 key
	for _, tg := range targets {
		if strings.HasSuffix(tg, ":win") {
			t.Errorf("PendingTargets 不应返回 :win 标记 key, got %q", tg)
		}
	}
}

// TestAggregator_FlushDedupAfterFlush Flush 后数据被删，再 Flush 返回空（不重复发）。
func TestAggregator_FlushDedupAfterFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()
	a := NewAggregator(rc, 30*time.Second)
	ctx := context.Background()
	_, _ = a.Add(ctx, "user:9", "warning", AggregatedItem{IncidentID: 9})

	mr.FastForward(31 * time.Second)
	items1, _ := a.Flush(ctx, "user:9")
	if len(items1) != 1 {
		t.Fatalf("first flush: got %d, want 1", len(items1))
	}
	items2, _ := a.Flush(ctx, "user:9")
	if len(items2) != 0 {
		t.Errorf("second flush should be empty (already drained), got %d", len(items2))
	}
}
