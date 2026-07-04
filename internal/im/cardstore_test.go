package im

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newRedisTestStore 启动 miniredis 返回连接它的 RedisCardStore + 底层 client（供模拟重启复用同一后端）。
func newRedisTestStore(t *testing.T) (*RedisCardStore, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisCardStore(rc), rc
}

// TestRedisCardStore_PutGet Redis 实现基本存取。
func TestRedisCardStore_PutGet(t *testing.T) {
	ctx := context.Background()
	s, _ := newRedisTestStore(t)
	s.Put(ctx, 42, "feishu", "card_abc")
	if id, ok := s.Get(ctx, 42, "feishu"); !ok || id != "card_abc" {
		t.Errorf("Get: got %q ok=%v, want card_abc true", id, ok)
	}
	// 未写入的 (incident, platform) 组合应 miss。
	if _, ok := s.Get(ctx, 42, "dingtalk"); ok {
		t.Error("dingtalk should not exist")
	}
	if _, ok := s.Get(ctx, 99, "feishu"); ok {
		t.Error("incident 99 should not exist")
	}
}

// TestRedisCardStore_MultiPlatform 同一 incident 多平台各自独立存取。
func TestRedisCardStore_MultiPlatform(t *testing.T) {
	ctx := context.Background()
	s, _ := newRedisTestStore(t)
	s.Put(ctx, 7, "feishu", "fs_1")
	s.Put(ctx, 7, "dingtalk", "dt_1")
	if id, _ := s.Get(ctx, 7, "feishu"); id != "fs_1" {
		t.Errorf("feishu card: got %q, want fs_1", id)
	}
	if id, _ := s.Get(ctx, 7, "dingtalk"); id != "dt_1" {
		t.Errorf("dingtalk card: got %q, want dt_1", id)
	}
}

// TestRedisCardStore_PersistsAcrossRestart B24 核心：进程重启后已发卡片映射仍在，卡片可刷新。
// 模拟方式：用一个 store 实例写入，再新建一个 store 实例（=进程重启后重新装配）指向同一 Redis，
// 验证新实例仍能读到旧映射——这正是内存实现做不到、B24 要修的点。
func TestRedisCardStore_PersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)

	// —— 「重启前」：实例 A 写入卡片映射 ——
	rcA := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	storeA := NewRedisCardStore(rcA)
	storeA.Put(ctx, 100, "feishu", "card_before_restart")
	_ = rcA.Close() // 模拟旧进程退出

	// —— 「重启后」：实例 B 全新装配，指向同一 Redis 后端 ——
	rcB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rcB.Close() })
	storeB := NewRedisCardStore(rcB)

	id, ok := storeB.Get(ctx, 100, "feishu")
	if !ok || id != "card_before_restart" {
		t.Errorf("重启后应仍能读到旧卡片映射，got %q ok=%v, want card_before_restart true", id, ok)
	}
}

// TestRedisCardStore_NilClient nil client 不 panic，Get 恒 miss、Put 无副作用（降级安全）。
func TestRedisCardStore_NilClient(t *testing.T) {
	ctx := context.Background()
	s := NewRedisCardStore(nil)
	s.Put(ctx, 1, "feishu", "x") // 不应 panic
	if _, ok := s.Get(ctx, 1, "feishu"); ok {
		t.Error("nil client 应恒 miss")
	}
}

// TestCardStore_InterfaceSatisfied 两种实现都满足 CardStore 接口（编译期 + 行为一致性）。
func TestCardStore_InterfaceSatisfied(t *testing.T) {
	ctx := context.Background()
	var stores = []CardStore{NewCardStore(), func() CardStore { s, _ := newRedisTestStore(t); return s }()}
	for i, s := range stores {
		s.Put(ctx, 5, "feishu", "cid")
		if id, ok := s.Get(ctx, 5, "feishu"); !ok || id != "cid" {
			t.Errorf("store[%d]: Get got %q ok=%v, want cid true", i, id, ok)
		}
	}
}
