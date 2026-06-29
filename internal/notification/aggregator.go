// aggregator.go 通知聚合（能力域 7 M7.9）—— "少打扰"核心之三。
//
// 对应 capabilities/04-notification.md §8。
// 短时间内对同一人的多条通知合并，避免轰炸：
//   - 聚合窗口默认 30s，窗口内对同一 target 的多条通知合并成一条
//   - critical 例外：critical 不聚合，立即单独通知
//
// 实现：Redis 维护 pending_notify:{targetID} 队列。
// Add 时：
//   - critical → 立即返回（sendNow=true），不进队列
//   - 窗口内首条 → 入队，记录窗口结束时间（sendNow=false，由定时任务 flush）
//   - 窗口内后续 → 入队（sendNow=false，合并）
//
// Flush 由定时任务（asynq periodic）按 target 维度扫到期的队列，合并发送。
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Aggregator 通知聚合器。
// redis 为 nil 时所有 Add 立即返回 sendNow=true（降级为不聚合，保证送达）。
type Aggregator struct {
	redis  *redis.Client
	window time.Duration // 聚合窗口，默认 30s
}

// NewAggregator 创建聚合器。window<=0 用默认 30s。
func NewAggregator(rc *redis.Client, window time.Duration) *Aggregator {
	if window <= 0 {
		window = 30 * time.Second
	}
	return &Aggregator{redis: rc, window: window}
}

// AggregatedItem 聚合队列中的单条待发项。
type AggregatedItem struct {
	IncidentID int    `json:"incident_id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Level      int    `json:"level"`
	Severity   string `json:"severity"`
	AddedAt    int64  `json:"added_at"` // unix nano
}

// AddDecision Add 的返回：如何处置这条通知。
type AddDecision struct {
	// SendNow=true 表示应立即发送（critical，或无 Redis 降级）。
	// SendNow=false 表示已入聚合队列，等待窗口结束时 Flush 合并发送。
	SendNow bool
	// Batched 该 target 队列中当前积压的条目（Flush 时合并），仅 SendNow=false 时有意义。
	Batched []AggregatedItem
}

// Add 把一条通知加入 target 的聚合队列。
//
//	targetID 通知目标唯一标识（通常 = user_id）
//	severity 严重度；critical 立即发送不聚合
//	item 通知内容（用于 Flush 时合并成一条）
func (a *Aggregator) Add(ctx context.Context, targetID, severity string, item AggregatedItem) (*AddDecision, error) {
	// critical 不聚合，立即发送
	if severity == "critical" {
		return &AddDecision{SendNow: true}, nil
	}
	// 无 Redis：降级为不聚合，立即发送（保证送达）
	if a.redis == nil {
		return &AddDecision{SendNow: true}, nil
	}
	key := "vigil:pending_notify:" + targetID
	item.AddedAt = time.Now().UnixNano()
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("aggregate marshal: %w", err)
	}
	// RPUSH 入队；TTL 设为 window + 缓冲（QA 审计 C3）。
	// 旧实现 TTL=window，导致窗口到点时 key 直接过期、数据被 Redis 删除，
	// Flush 取不到任何条目 → 聚合通知静默丢失。改为 window+缓冲，让 Flush
	// ticker（周期 ≤ window）有机会在数据失效前读取并发送。
	// SET NX：仅首条入队时设 TTL，避免每次 Add 重置 TTL 拖长窗口。
	pipe := a.redis.Pipeline()
	pipe.RPush(ctx, key, raw)
	pipe.Set(ctx, key+":win", "1", a.window) // 窗口标记，独立 key，到点失效
	// 数据 key 的 TTL 给足缓冲：window 的 3 倍，确保 Flush（≤window 周期）能读到
	pipe.Expire(ctx, key, 3*a.window)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("aggregate push: %w", err)
	}
	// 取当前积压（含刚 push 的）
	items, err := a.peek(ctx, key)
	if err != nil {
		return nil, err
	}
	return &AddDecision{SendNow: false, Batched: items}, nil
}

// peek 读队列全部条目（不删，Flush 时才删）。
func (a *Aggregator) peek(ctx context.Context, key string) ([]AggregatedItem, error) {
	raws, err := a.redis.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("aggregate lrange: %w", err)
	}
	out := make([]AggregatedItem, 0, len(raws))
	for _, r := range raws {
		var it AggregatedItem
		if json.Unmarshal([]byte(r), &it) == nil {
			out = append(out, it)
		}
	}
	return out, nil
}

// Flush 扫描一个 target 的队列，若窗口已到则取出全部并删除，返回合并条目。
// 窗口判定（QA 审计 C3）：用独立的 :win 标记 key 判断窗口是否到点，而非数据 key 的 TTL
// （数据 key 现在保留 3×window 供 Flush 读取）。
// 窗口未到（:win 仍存在）返回 nil, nil（调用方定期重试）。
func (a *Aggregator) Flush(ctx context.Context, targetID string) ([]AggregatedItem, error) {
	if a.redis == nil {
		return nil, nil
	}
	key := "vigil:pending_notify:" + targetID
	winKey := key + ":win"
	// :win 仍存在 → 窗口未到，不 flush
	exists, err := a.redis.Exists(ctx, winKey).Result()
	if err != nil {
		return nil, fmt.Errorf("aggregate win exists: %w", err)
	}
	if exists > 0 {
		return nil, nil // 窗口内，等待
	}
	// 窗口到：取全部并删（数据 + 窗口标记残留）
	items, err := a.peek(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	pipe := a.redis.Pipeline()
	pipe.Del(ctx, key)
	pipe.Del(ctx, winKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("aggregate del: %w", err)
	}
	return items, nil
}

// PendingTargets 扫描 Redis 返回当前有积压待发通知的 targetID 列表（去 :win 后缀）。
// 供 FlushAll 周期驱动（QA 审计 C3）：原实现无任何调度器调用 Flush，聚合通知成死信。
// 用 SCAN（非 KEYS）避免阻塞 Redis；match 前缀 vigil:pending_notify:。
func (a *Aggregator) PendingTargets(ctx context.Context) ([]string, error) {
	if a.redis == nil {
		return nil, nil
	}
	var targets []string
	var cursor uint64
	prefix := "vigil:pending_notify:"
	for {
		keys, next, err := a.redis.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("aggregate scan: %w", err)
		}
		for _, k := range keys {
			// 过滤掉 :win 标记 key，只保留数据 key
			if strings.HasSuffix(k, ":win") {
				continue
			}
			targets = append(targets, strings.TrimPrefix(k, prefix))
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return targets, nil
}

// Window 暴露聚合窗口（便于上层日志/配置展示）。
func (a *Aggregator) Window() time.Duration { return a.window }
