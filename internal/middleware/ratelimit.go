// Package middleware 提供通用中间件（限流等）。
//
// 限流实现从 internal/ai/cost.go 的 allowRateLimit 抽取通用化，
// 供 ingestion webhook（按 Integration）、通用 API（按 IP/user）等场景复用。
package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter Redis 滑动窗口限流器（无状态，持有 redis client）。
//
// 算法：ZSET 存窗口内请求的时间戳，每次请求：
//  1. 删除窗口外的旧成员（ZRemRangeByScore）
//  2. 计数当前窗口成员（ZCard）
//  3. 未超限则添加当前请求（ZAdd）
//
// 窗口固定 1 分钟（与 oncall 产品常见的"每分钟 N 次"语义一致）。
// 无 Redis 时 Allow 恒返回 true（降级不限流，保证可用性优先）。
type Limiter struct {
	redis *redis.Client
}

// NewLimiter 构造限流器。rc 为 nil 时降级为不限流。
func NewLimiter(rc *redis.Client) *Limiter {
	return &Limiter{redis: rc}
}

// Available 是否启用限流（redis 已配置）。
func (l *Limiter) Available() bool { return l != nil && l.redis != nil }

// Allow 判断 key（如 "integration:5" / "ip:1.2.3.4"）在窗口内是否允许第 N 次请求。
// limit 为窗口内最大次数，<=0 表示不限流（恒放行）。
// 返回 (allowed, error)：Redis 故障时降级返回 true（可用性优先，调用方可记日志）。
func (l *Limiter) Allow(ctx context.Context, key string, limit int) (bool, error) {
	if l == nil || l.redis == nil || limit <= 0 {
		return true, nil
	}
	fullKey := "vigil:ratelimit:" + key
	now := time.Now().UnixNano()
	windowStart := time.Now().Add(-time.Minute).UnixNano()

	pipe := l.redis.Pipeline()
	pipe.ZRemRangeByScore(ctx, fullKey, "0", strconv.FormatInt(windowStart, 10))
	countCmd := pipe.ZCard(ctx, fullKey)
	pipe.ZAdd(ctx, fullKey, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, fullKey, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return true, nil // Redis 故障降级：放行（可用性优先）
	}
	return countCmd.Val() < int64(limit), nil
}

// BackpressureChecker 队列积压背压检查器。
// 队列深度超阈值时，接入层应返回 503 但 payload 仍落库（不丢告警）。
type BackpressureChecker struct {
	redis    *redis.Client
	asynqKey string // Asynq 队列在 Redis 的前缀（默认 asynq，列表 key 形如 asynq:critical）
	maxDepth int    // 积压阈值，超过视为背压
}

// NewBackpressureChecker 构造背压检查器。maxDepth<=0 表示不检查。
func NewBackpressureChecker(rc *redis.Client, maxDepth int) *BackpressureChecker {
	return &BackpressureChecker{redis: rc, asynqKey: "asynq", maxDepth: maxDepth}
}

// Available 是否启用背压检查。
func (b *BackpressureChecker) Available() bool { return b != nil && b.redis != nil && b.maxDepth > 0 }

// IsOverloaded 检查队列是否积压超阈值。
// 实现：Asynq 用 Redis list 存待处理任务（key 形如 asynq:critical），LLEN 各队列求和。
// 任何 Redis 故障返回 false（不触发背压，保证可用性）。
func (b *BackpressureChecker) IsOverloaded(ctx context.Context) bool {
	if !b.Available() {
		return false
	}
	// Asynq 队列名：critical / default / low（见 queue.go Config.Queues）
	queues := []string{"critical", "default", "low"}
	var total int64
	for _, q := range queues {
		n, err := b.redis.LLen(ctx, b.asynqKey+":"+q).Result()
		if err != nil {
			return false // 故障不触发背压
		}
		total += n
	}
	return total > int64(b.maxDepth)
}
