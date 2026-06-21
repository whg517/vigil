package notification

import (
	"context"
	"testing"
	"time"
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
