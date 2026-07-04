// cardstore.go incident → 已发卡片 ID 的映射存储（供状态变更后 UpdateCard 刷新）。
//
// B24 持久化：原实现是进程内存 map，进程重启后映射丢失 —— 已发出的卡片再也无法
// 通过 UpdateCard 刷新（状态永停在下发时的样子）。本文件把 CardStore 抽象为接口，
// 提供两种实现：
//   - MemoryCardStore：进程内 map（无 Redis 时的降级兜底，单副本可用）。
//   - RedisCardStore：Redis 持久化（重启后卡片仍可刷新，复用项目 Redis 客户端）。
//
// 依赖注入方向：handler/refresher/channel 只依赖 CardStore 接口，装配层（wire.go）
// 按 Redis 是否可用选择实现。
package im

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// CardStore 记录 incident → 已发卡片 ID 的映射，供状态变更后 UpdateCard。
//
// 方法带 ctx：Redis 实现需要它做超时/取消控制；内存实现忽略。
type CardStore interface {
	// Put 记录某 incident 在某平台下发的卡片 ID。
	Put(ctx context.Context, incidentID int, platform, cardID string)
	// Get 取某 incident 在某平台的卡片 ID（ok=false 表示无记录）。
	Get(ctx context.Context, incidentID int, platform string) (string, bool)
}

// MemoryCardStore 进程内存实现（重启丢失）。
// 无 Redis 时的降级兜底；单副本部署下可用，多副本/重启场景请用 RedisCardStore。
type MemoryCardStore struct {
	mu    sync.RWMutex
	cards map[int]map[string]string // incidentID → platform → cardID
}

// NewCardStore 创建内存卡片 ID 存储（默认实现，向后兼容）。
func NewCardStore() *MemoryCardStore {
	return &MemoryCardStore{cards: make(map[int]map[string]string)}
}

// Put 记录某 incident 在某平台下发的卡片 ID。
func (s *MemoryCardStore) Put(_ context.Context, incidentID int, platform, cardID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cards[incidentID] == nil {
		s.cards[incidentID] = make(map[string]string)
	}
	s.cards[incidentID][platform] = cardID
}

// Get 取某 incident 在某平台的卡片 ID。
func (s *MemoryCardStore) Get(_ context.Context, incidentID int, platform string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.cards[incidentID]; ok {
		if id, ok2 := m[platform]; ok2 {
			return id, true
		}
	}
	return "", false
}

// cardStoreTTL 卡片映射在 Redis 的存活时长。
// 卡片刷新只在 Incident 活跃期（未关闭）有意义；关闭后不再刷新，无需长期占用。
// 取 7 天：覆盖典型处置周期 + 复盘窗口，过期自动回收，无需显式清理。
const cardStoreTTL = 7 * 24 * time.Hour

// RedisCardStore Redis 持久化实现（B24）。
// key 形如 "vigil:im:card:{incidentID}"，field=platform，value=cardID（HASH 结构）。
// 重启后映射仍在，已发卡片可继续 UpdateCard 刷新。
type RedisCardStore struct {
	rc *redis.Client
}

// NewRedisCardStore 创建 Redis 卡片存储。rc 为项目共享 Redis 客户端。
func NewRedisCardStore(rc *redis.Client) *RedisCardStore {
	return &RedisCardStore{rc: rc}
}

// cardStoreKey 卡片映射的 Redis key（按 incident 聚合，platform 作 HASH field）。
func cardStoreKey(incidentID int) string {
	return fmt.Sprintf("vigil:im:card:%d", incidentID)
}

// Put 记录卡片 ID 到 Redis（HSET + 刷新 TTL）。
// best-effort：写失败不阻塞主流程（卡片已下发，映射丢失最多导致后续无法刷新，可重发兜底）。
func (s *RedisCardStore) Put(ctx context.Context, incidentID int, platform, cardID string) {
	if s.rc == nil {
		return
	}
	key := cardStoreKey(incidentID)
	// HSET 写字段 + EXPIRE 刷新 TTL（每次下发/更新都续期，活跃单不会因 TTL 提前失联）。
	_ = s.rc.HSet(ctx, key, platform, cardID).Err()
	_ = s.rc.Expire(ctx, key, cardStoreTTL).Err()
}

// Get 从 Redis 取卡片 ID（HGET）。
// key/field 不存在或读失败均返回 ("", false)——调用方据此跳过刷新（不误刷）。
func (s *RedisCardStore) Get(ctx context.Context, incidentID int, platform string) (string, bool) {
	if s.rc == nil {
		return "", false
	}
	id, err := s.rc.HGet(ctx, cardStoreKey(incidentID), platform).Result()
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}
