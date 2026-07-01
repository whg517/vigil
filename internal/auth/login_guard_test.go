// login_guard_test.go 登录防护器测试（SEC-04）。
package auth

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newGuardTestClient 启动 miniredis 返回连接它的 guard（测试隔离）。
func newGuardTestClient(t *testing.T) (*LoginGuard, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewLoginGuard(rc), rc
}

// TestLoginGuard_Available nil 时不启用。
func TestLoginGuard_Available(t *testing.T) {
	var nilGuard *LoginGuard
	if nilGuard.Available() {
		t.Error("nil guard should not be Available")
	}
	g := NewLoginGuard(nil)
	if g.Available() {
		t.Error("guard with nil redis should not be Available")
	}
}

// TestLoginGuard_AllowNormal 正常尝试应放行（未达限）。
func TestLoginGuard_AllowNormal(t *testing.T) {
	g, _ := newGuardTestClient(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		allowed, _ := g.Check(ctx, "1.2.3.4", "alice")
		if !allowed {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
}

// TestLoginGuard_IPLimit 单 IP 维度超限应拒绝（username 不同也算同 IP）。
func TestLoginGuard_IPLimit(t *testing.T) {
	g, _ := newGuardTestClient(t)
	ctx := context.Background()
	// 同 IP 撞不同账号：IP 维度累加（防单点撞库）
	for i := 0; i < 10; i++ {
		allowed, _ := g.Check(ctx, "9.9.9.9", "user"+string(rune('a'+i)))
		if !allowed && i < 9 {
			t.Fatalf("attempt %d should pass", i)
		}
	}
	// 第 11 次（同 IP）应被拒
	allowed, reason := g.Check(ctx, "9.9.9.9", "another-user")
	if allowed {
		t.Error("11th attempt from same IP should be rate-limited")
	}
	if reason == "" {
		t.Error("rate-limited reason should not be empty")
	}
}

// TestLoginGuard_UserLimit 单 username 维度超限应拒绝。
func TestLoginGuard_UserLimit(t *testing.T) {
	g, _ := newGuardTestClient(t)
	ctx := context.Background()
	// 同账号从不同 IP（防针对账号爆破，即便换 IP 也算同账号）
	for i := 0; i < 10; i++ {
		allowed, _ := g.Check(ctx, "10.0.0."+string(rune('0'+i)), "bob")
		if !allowed && i < 9 {
			t.Fatalf("attempt %d should pass", i)
		}
	}
	// 第 11 次应被拒（账号维度）
	allowed, reason := g.Check(ctx, "10.0.0.99", "bob")
	if allowed {
		t.Error("11th attempt for same username should be rate-limited")
	}
	if reason == "" {
		t.Error("rate-limited reason should not be empty")
	}
}

// TestLoginGuard_FailLock 连续失败达阈值后账号被锁定。
func TestLoginGuard_FailLock(t *testing.T) {
	g, _ := newGuardTestClient(t)
	ctx := context.Background()
	// 模拟 5 次连续失败
	for i := 0; i < 5; i++ {
		g.RecordFailure(ctx, "carol")
	}
	// 第 6 次尝试：账号应被锁定（即便换 IP）
	allowed, reason := g.Check(ctx, "5.5.5.5", "carol")
	if allowed {
		t.Error("account should be locked after 5 failures")
	}
	if reason == "" {
		t.Error("lock reason should not be empty")
	}
}

// TestLoginGuard_SuccessClearsFails 登录成功清零失败计数（不触发锁定）。
func TestLoginGuard_SuccessClearsFails(t *testing.T) {
	g, _ := newGuardTestClient(t)
	ctx := context.Background()
	// 4 次失败（未达阈值 5）
	for i := 0; i < 4; i++ {
		g.RecordFailure(ctx, "dave")
	}
	// 成功登录：清零
	g.RecordSuccess(ctx, "dave")
	// 再 4 次失败：若计数已清零，则未达阈值，不应锁定
	for i := 0; i < 4; i++ {
		g.RecordFailure(ctx, "dave")
	}
	allowed, _ := g.Check(ctx, "1.1.1.1", "dave")
	if !allowed {
		t.Error("account should not be locked after success reset the fail counter")
	}
}

// TestLoginGuard_NilRedisDegrade 无 Redis 时降级放行（可用性优先）。
func TestLoginGuard_NilRedisDegrade(t *testing.T) {
	g := NewLoginGuard(nil)
	ctx := context.Background()
	allowed, reason := g.Check(ctx, "1.2.3.4", "eve")
	if !allowed {
		t.Error("nil redis should degrade to allow")
	}
	if reason != "" {
		t.Error("degraded reason should be empty")
	}
	// RecordFailure / RecordSuccess 在 nil redis 下应为 no-op（不 panic）
	g.RecordFailure(ctx, "eve")
	g.RecordSuccess(ctx, "eve")
}

// === ClientIP 提取 ===

func TestClientIP_ForwardedFor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4, 10.0.0.1", "1.2.3.4"},
		{"  1.2.3.4  , 10.0.0.1", "1.2.3.4"},
		{"1.2.3.4", "1.2.3.4"},
	}
	for _, c := range cases {
		if got := ClientIP(c.in, ""); got != c.want {
			t.Errorf("ClientIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:8080", "::1"},
		{"192.168.1.1:443", "192.168.1.1"},
	}
	for _, c := range cases {
		if got := ClientIP("", c.in); got != c.want {
			t.Errorf("ClientIP(remoteAddr=%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
