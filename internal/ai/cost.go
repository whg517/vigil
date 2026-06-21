// cost.go LLM 成本控制（能力域 11，capabilities/07 §B5 开放问题 Q1）。
//
// 三道闸（按调用顺序）：
//  1. 缓存：Redis 以 sha256(prompt) 为 key 缓存 Complete 结果，TTL 可配（默认 1h）。
//     命中直接返回，省一次真实调用 + token。
//  2. 限流：Redis ZSET 滑动窗口，按维度（org/team/user）每分钟最大请求数，超限拒绝（降级）。
//  3. 配额：Redis counter 累计 token 消耗，按维度达上限拒绝。
//
// 实现 Provider 接口：Complete 内部走 缓存→限流→配额→真实 provider；
// Embed 不缓存（向量检索场景语义稳定，重复 embed 由 ensureEmbedding 的回写持久化去重）。
//
// CostController 包装一个底层 Provider（如 GLMProvider）。无 Redis 时缓存/限流/配额
// 全部跳过（降级为透传），仅保证调用可达。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kevin/vigil/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// CostConfig 成本控制配置。
type CostConfig struct {
	// CacheTTL 缓存有效期，<=0 用默认 1h；为 0 且 DisableCache=true 不缓存。
	CacheTTL time.Duration
	// DisableCache 关闭缓存（调试用）。
	DisableCache bool
	// RateLimitPerMin 每分钟最大请求数（按维度），<=0 不限流。
	RateLimitPerMin int
	// TokenQuota token 配额（按维度累计，达上限拒绝），<=0 不限额。
	TokenQuota int
}

// CostController LLM 成本控制包装器，实现 Provider。
type CostController struct {
	inner     Provider      // 底层真实 provider
	redis     *redis.Client // 可为 nil（降级）
	cfg       CostConfig
	dimension string // 配额/限流维度标识（如 "org:1"），key 前缀
}

// NewCostController 创建成本控制器，包装底层 provider。
// dimension 为配额/限流维度标识（如 "org:default"），用于 Redis key 命名。
func NewCostController(inner Provider, rc *redis.Client, dimension string, cfg CostConfig) *CostController {
	if cfg.CacheTTL <= 0 && !cfg.DisableCache {
		cfg.CacheTTL = time.Hour
	}
	if dimension == "" {
		dimension = "default"
	}
	return &CostController{inner: inner, redis: rc, cfg: cfg, dimension: dimension}
}

// Available 透传底层 provider。
func (c *CostController) Available() bool {
	if c.inner == nil {
		return false
	}
	return c.inner.Available()
}

// Complete 缓存→限流→配额→真实调用。
func (c *CostController) Complete(ctx context.Context, prompt string) (string, error) {
	// 1. 缓存
	if !c.cfg.DisableCache && c.redis != nil {
		key := c.cacheKey(prompt)
		if cached, err := c.redis.Get(ctx, key).Result(); err == nil {
			metrics.LLMCacheHits.Inc()
			metrics.LLMCalls.WithLabelValues("complete", "cache_hit").Inc()
			return cached, nil
		}
	}

	// 2. 限流
	if c.redis != nil && c.cfg.RateLimitPerMin > 0 {
		ok, err := c.allowRateLimit(ctx)
		if err == nil && !ok {
			metrics.LLMRateLimited.Inc()
			metrics.LLMCalls.WithLabelValues("complete", "rate_limited").Inc()
			return "", errors.New("llm rate limited")
		}
	}

	// 3. 配额预检
	if c.redis != nil && c.cfg.TokenQuota > 0 {
		used, _ := c.tokensUsed(ctx)
		if used >= c.cfg.TokenQuota {
			metrics.LLMRateLimited.Inc()
			metrics.LLMCalls.WithLabelValues("complete", "quota_exceeded").Inc()
			return "", errors.New("llm token quota exceeded")
		}
	}

	// 4. 真实调用
	out, err := c.inner.Complete(ctx, prompt)
	if err != nil {
		metrics.LLMCalls.WithLabelValues("complete", "error").Inc()
		return "", err
	}
	metrics.LLMCalls.WithLabelValues("complete", "success").Inc()

	// 5. 回写缓存
	if !c.cfg.DisableCache && c.redis != nil {
		_ = c.redis.Set(ctx, c.cacheKey(prompt), out, c.cfg.CacheTTL).Err()
	}
	return out, nil
}

// Embed 透传（带限流），不缓存（向量检索语义稳定，由 ensureEmbedding 持久化去重）。
func (c *CostController) Embed(ctx context.Context, text string) ([]float32, error) {
	// Embed 也限流（向量调用同样耗 token）
	if c.redis != nil && c.cfg.RateLimitPerMin > 0 {
		if ok, err := c.allowRateLimit(ctx); err == nil && !ok {
			metrics.LLMRateLimited.Inc()
			return nil, errors.New("llm rate limited")
		}
	}
	out, err := c.inner.Embed(ctx, text)
	if err == nil && len(out) > 0 {
		// 估算 token：粗略按向量维度计（embedding-3 输入 token ~ 输入文本长度/4）。
		// 这里用 len(out) 作 token 估算埋点（不准确但用于配额/趋势观察）。
		metrics.LLMTokensTotal.WithLabelValues("embedding").Add(float64(len(out)))
	}
	return out, err
}

// --- Redis 辅助 ---

// cacheKey 缓存 key：sha256(prompt) 避免长 prompt 入 key。
func (c *CostController) cacheKey(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return "vigil:llm:cache:" + hex.EncodeToString(sum[:])
}

// allowRateLimit 滑动窗口限流（Redis ZSET）。
// 窗口 1 分钟：删掉 1 分钟前的成员，计数当前窗口内成员数，未超限则加一个。
func (c *CostController) allowRateLimit(ctx context.Context) (bool, error) {
	key := "vigil:llm:ratelimit:" + c.dimension
	now := time.Now().UnixNano()
	windowStart := time.Now().Add(-time.Minute).UnixNano()
	pipe := c.redis.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))
	countCmd := pipe.ZCard(ctx, key)
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, key, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return countCmd.Val() < int64(c.cfg.RateLimitPerMin), nil
}

// tokensUsed 当前维度已用 token（Redis counter）。
func (c *CostController) tokensUsed(ctx context.Context) (int, error) {
	key := "vigil:llm:tokens:" + c.dimension
	v, err := c.redis.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return v, err
}

// AddTokens 累加 token 用量（供 Complete 后调用方按真实 usage 计费）。
// Vigil 内部 LLM 调用若返回 usage，调用此方法更新配额。
func (c *CostController) AddTokens(ctx context.Context, tokens int) error {
	if c.redis == nil || c.cfg.TokenQuota <= 0 || tokens <= 0 {
		return nil
	}
	key := "vigil:llm:tokens:" + c.dimension
	if err := c.redis.IncrBy(ctx, key, int64(tokens)).Err(); err != nil {
		return fmt.Errorf("add tokens: %w", err)
	}
	return nil
}
