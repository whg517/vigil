package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCostController_NoRedis_NoCache 无 Redis 时透传（缓存/限流/配额全跳过）。
func TestCostController_NoRedis_NoCache(t *testing.T) {
	mp := &mockProvider{resp: "answer", avail: true}
	cc := NewCostController(mp, nil, "test", CostConfig{})
	out, err := cc.Complete(context.Background(), "q1")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "answer" {
		t.Errorf("got %q, want answer", out)
	}
	// 无 Redis 不缓存，两次调用都应真实执行（mp.called==2）
	_, _ = cc.Complete(context.Background(), "q1")
	if mp.called != 2 {
		t.Errorf("无 Redis 应每次真实调用，called=%d, want 2", mp.called)
	}
}

// TestCostController_Available_TunnelsInner 底层不可用时 Available=false。
func TestCostController_Available_TunnelsInner(t *testing.T) {
	cc := NewCostController(&mockProvider{avail: false}, nil, "x", CostConfig{})
	if cc.Available() {
		t.Error("底层不可用时 CostController.Available 应 false")
	}
	cc2 := NewCostController(nil, nil, "x", CostConfig{})
	if cc2.Available() {
		t.Error("nil inner 时 Available 应 false")
	}
}

// TestCostController_DisableCache DisableCache=true 时即使有 Redis 逻辑也透传（这里用 nil redis 验证路径）。
func TestCostController_DisableCache(t *testing.T) {
	mp := &mockProvider{resp: "x", avail: true}
	cc := NewCostController(mp, nil, "x", CostConfig{DisableCache: true})
	if _, err := cc.Complete(context.Background(), "q"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if mp.called != 1 {
		t.Errorf("disable cache 应真实调用一次，got %d", mp.called)
	}
}

// TestCostController_DefaultCacheTTL 未配 CacheTTL 时默认 1h。
func TestCostController_DefaultCacheTTL(t *testing.T) {
	cc := NewCostController(&mockProvider{avail: true}, nil, "x", CostConfig{})
	if cc.cfg.CacheTTL != time.Hour {
		t.Errorf("默认 CacheTTL 应 1h，got %v", cc.cfg.CacheTTL)
	}
}

// TestCostController_EmbedTunnels Embed 透传到底层（不缓存）。
func TestCostController_EmbedTunnels(t *testing.T) {
	mp := &mockProvider{avail: true}
	cc := NewCostController(mp, nil, "x", CostConfig{})
	vec, err := cc.Embed(context.Background(), "text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("Embed 应透传底层 mock 返回 3 维，got %d", len(vec))
	}
}

// TestCostController_InnerError 底层 provider 出错时透传错误。
func TestCostController_InnerError(t *testing.T) {
	mp := &mockProvider{err: errors.New("network"), avail: true}
	cc := NewCostController(mp, nil, "x", CostConfig{})
	_, err := cc.Complete(context.Background(), "q")
	if err == nil {
		t.Error("底层出错时应透传错误")
	}
}

// TestCacheKey 同 prompt 同 key（稳定）。
func TestCacheKey(t *testing.T) {
	cc := NewCostController(&mockProvider{avail: true}, nil, "x", CostConfig{})
	k1 := cc.cacheKey("hello")
	k2 := cc.cacheKey("hello")
	k3 := cc.cacheKey("world")
	if k1 != k2 {
		t.Error("同 prompt 应同 key")
	}
	if k1 == k3 {
		t.Error("不同 prompt 应不同 key")
	}
	if !strings.HasPrefix(k1, "vigil:llm:cache:") {
		t.Errorf("key 前缀错误: %s", k1)
	}
}
