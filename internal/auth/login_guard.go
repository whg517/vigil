// login_guard.go 登录限流与账户锁定（SEC-04）。
//
// 防暴力破解：双维度限流 + 失败计数锁定。
//   - IP 维度：单 IP 每分钟最多 N 次登录尝试（防单点撞库）
//   - 用户名维度：单账号每分钟最多 N 次登录尝试（防针对账号爆破）
//   - 失败锁定：某账号连续失败 M 次后短期锁定（即便密码正确也拒登）
//
// 复用 middleware.Limiter 的滑动窗口算法，但独立键空间 + 独立语义
// （auth 包不反向依赖 middleware 包，保持依赖方向：middleware 依赖 auth 的 Permission，
// 反之会让 auth → middleware 形成潜在耦合；故此处直接持 redis.Client）。
//
// 降级策略与全局一致：无 Redis 时全部放行（可用性优先，依赖审计日志事后追溯）。
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LoginGuard 登录防护器（限流 + 失败锁定）。
//
// 零值不可用，须用 NewLoginGuard 构造。rc 为 nil 时所有检查恒放行（降级）。
type LoginGuard struct {
	redis *redis.Client
	// maxAttemptsPerMin 单维度（IP / username）每分钟最大尝试次数。
	maxAttemptsPerMin int
	// maxFails 连续失败达到此次数后锁定该 username。
	maxFails int
	// lockTTL 锁定持续时间。
	lockTTL time.Duration
}

// NewLoginGuard 构造登录防护器。rc 为 nil 时降级为不限制。
func NewLoginGuard(rc *redis.Client) *LoginGuard {
	return &LoginGuard{
		redis:             rc,
		maxAttemptsPerMin: 10, // 每分钟单维度 10 次尝试（覆盖正常用户输错重试）
		maxFails:          5,  // 连续失败 5 次锁定
		lockTTL:           5 * time.Minute,
	}
}

// Available 是否启用登录防护（redis 已配置）。
func (g *LoginGuard) Available() bool { return g != nil && g.redis != nil }

// Check 在登录尝试前校验：IP / username 是否超限或被锁定。
// 返回 (allowed, reason)：allowed=false 时 reason 描述限制原因（供 429 响应）。
// Redis 故障降级返回 (true, "")（可用性优先）。
func (g *LoginGuard) Check(ctx context.Context, ip, username string) (bool, string) {
	if !g.Available() {
		return true, ""
	}
	// 1. 失败锁定优先：username 被锁则直接拒
	if locked, _ := g.redis.Exists(ctx, g.lockKey(username)).Result(); locked > 0 {
		return false, "account temporarily locked due to repeated failures, retry later"
	}
	// 2. IP 维度限流（防单 IP 撞库）
	if ip != "" {
		if allowed := g.allow(ctx, g.ipKey(ip), g.maxAttemptsPerMin); !allowed {
			return false, "too many login attempts from this IP"
		}
	}
	// 3. username 维度限流（防针对账号爆破）
	if username != "" {
		if allowed := g.allow(ctx, g.userKey(username), g.maxAttemptsPerMin); !allowed {
			return false, "too many login attempts for this account"
		}
	}
	return true, ""
}

// RecordFailure 记录一次登录失败：累加 username 失败计数，达阈值则设锁定。
// 与滑动窗口限流解耦——锁定是"连续失败"的累加语义，不是窗口内计数。
func (g *LoginGuard) RecordFailure(ctx context.Context, username string) {
	if !g.Available() || username == "" {
		return
	}
	key := g.failKey(username)
	// INCR + 首次设 TTL（窗口 5 分钟：5 分钟内未再失败则计数衰减）
	n, _ := g.redis.Incr(ctx, key).Result()
	if n == 1 {
		g.redis.Expire(ctx, key, g.lockTTL)
	}
	// 达阈值：设锁定标记 + 清零计数（锁定期内即便继续请求也不累加）
	if n >= int64(g.maxFails) {
		g.redis.Set(ctx, g.lockKey(username), "1", g.lockTTL)
		g.redis.Del(ctx, key)
	}
}

// RecordSuccess 登录成功清零失败计数（密码正确即视为正常用户）。
func (g *LoginGuard) RecordSuccess(ctx context.Context, username string) {
	if !g.Available() || username == "" {
		return
	}
	g.redis.Del(ctx, g.failKey(username))
}

// allow 滑动窗口限流：key 在 1 分钟窗口内是否允许新增一次。
// 算法与 middleware.Limiter.Allow 一致（ZSET 时间戳），独立键空间避免互相影响。
func (g *LoginGuard) allow(ctx context.Context, fullKey string, limit int) bool {
	if limit <= 0 {
		return true
	}
	now := time.Now().UnixNano()
	windowStart := time.Now().Add(-time.Minute).UnixNano()
	pipe := g.redis.Pipeline()
	pipe.ZRemRangeByScore(ctx, fullKey, "0", fmt.Sprintf("%d", windowStart))
	countCmd := pipe.ZCard(ctx, fullKey)
	pipe.ZAdd(ctx, fullKey, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, fullKey, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return true // Redis 故障降级放行
	}
	return countCmd.Val() < int64(limit)
}

// 键命名集中管理（避免散落拼接出错）。
func (g *LoginGuard) ipKey(ip string) string  { return "vigil:login:ip:" + ip }
func (g *LoginGuard) userKey(u string) string { return "vigil:login:user:" + u }
func (g *LoginGuard) failKey(u string) string { return "vigil:login:fail:" + u }
func (g *LoginGuard) lockKey(u string) string { return "vigil:login:lock:" + u }

// ClientIP 从 echo.Context 提取客户端 IP 的最小依赖版本（避免 auth 包依赖 echo）。
// 优先 X-Forwarded-For 首段（反向代理场景），否则 RemoteAddr。
// 仅供 LoginGuard 在登录链路使用；通用 IP 提取应在 middleware 层。
func ClientIP(forwardedFor, remoteAddr string) string {
	if forwardedFor != "" {
		// X-Forwarded-For: client, proxy1, proxy2 —— 取第一个（最原始客户端）
		for i := 0; i < len(forwardedFor); i++ {
			if forwardedFor[i] == ',' {
				return trimmed(forwardedFor[:i])
			}
		}
		return trimmed(forwardedFor)
	}
	// RemoteAddr 形如 1.2.3.4:5678，去掉端口
	return trimmed(stripPort(remoteAddr))
}

// trimmed 去首尾空白（避免手写 strings.TrimSpace 增加导入；本文件已够多）。
func trimmed(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// stripPort 去掉 host:port 的端口部分（IPv6 形如 [::1]:8080 也兼容，去方括号）。
func stripPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host := addr[:i]
			// IPv6 含方括号 [::1]，剥掉首尾
			if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
				return host[1 : len(host)-1]
			}
			return host
		}
	}
	return addr
}
