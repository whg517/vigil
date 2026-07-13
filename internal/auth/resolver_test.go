// resolver_test.go X-Vigil-User-ID 头回退门控测试（SEC-02 修订）。
//
// 核心断言：伪造头默认**不再**生效——回退必须经 VIGIL_AUTH_HEADER_FALLBACK 显式开启，
// 且生产环境下即使显式开启也无效（config.Auth.EffectiveHeaderFallback 强制 false）。
package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/kevin/vigil/internal/config"
)

// forgedHeader 构造带伪造 X-Vigil-User-ID 的请求头。
func forgedHeader(uid string) http.Header {
	h := http.Header{}
	h.Set("X-Vigil-User-ID", uid)
	return h
}

// TestResolve_ForgedHeaderRejectedByDefault 验证默认配置下伪造头不生效。
// 默认配置 = HeaderFallback false（envconfig default）→ 装配层传 headerFallback=false。
func TestResolve_ForgedHeaderRejectedByDefault(t *testing.T) {
	// 与 wire.go 同款装配路径：默认配置 → EffectiveHeaderFallback → resolver。
	cfg := config.Auth{} // 零值 = envconfig default（HeaderFallback=false）
	r := NewIdentityResolver(nil, nil, cfg.EffectiveHeaderFallback(false), nil)

	uid, ok := r.Resolve(context.Background(), forgedHeader("42"))
	if ok {
		t.Fatalf("forged X-Vigil-User-ID must NOT resolve by default, got uid=%d", uid)
	}
}

// TestResolve_HeaderFallbackExplicitlyEnabled 验证显式开启后头回退生效（本地开发场景）。
func TestResolve_HeaderFallbackExplicitlyEnabled(t *testing.T) {
	cfg := config.Auth{HeaderFallback: true}
	r := NewIdentityResolver(nil, nil, cfg.EffectiveHeaderFallback(false), nil)

	uid, ok := r.Resolve(context.Background(), forgedHeader("42"))
	if !ok || uid != 42 {
		t.Fatalf("explicitly enabled fallback should resolve uid=42, got uid=%d ok=%v", uid, ok)
	}
}

// TestResolve_ProductionForcesFallbackOff 验证生产环境下显式开启也无效（双保险）。
func TestResolve_ProductionForcesFallbackOff(t *testing.T) {
	cfg := config.Auth{HeaderFallback: true}
	r := NewIdentityResolver(nil, nil, cfg.EffectiveHeaderFallback(true), nil)

	uid, ok := r.Resolve(context.Background(), forgedHeader("42"))
	if ok {
		t.Fatalf("production must force header fallback off even when enabled, got uid=%d", uid)
	}
}

// TestResolve_FallbackDisabled_NoHeader 验证回退关闭 + 无任何凭证时正常拒绝（回归保护）。
func TestResolve_FallbackDisabled_NoHeader(t *testing.T) {
	r := NewIdentityResolver(nil, nil, false, nil)
	if _, ok := r.Resolve(context.Background(), http.Header{}); ok {
		t.Fatal("no credential should not resolve")
	}
}
