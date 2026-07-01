// resolver.go 身份解析聚合器（能力域 13）。
//
// 统一 JWT / API Key / X-Vigil-User-ID 三轨身份解析，供中间件使用。
// 替代给中间件函数逐个传 signer/verifier 的参数膨胀方案。
//
// 解析顺序（优先级）：
//  1. Authorization: Bearer <jwt>   —— JWT 登录态（人，Web/IM）
//  2. X-Vigil-Key: <apikey>         —— API Key（程序化接入）
//  3. X-Vigil-User-ID: <uid>        —— 降级兼容（仅本地开发/测试，生产禁用）
//
// 安全约束（关键）：
//   - 任一凭证"存在但无效"时，不回退到更低优先级的凭证。
//     例：带了无效 Bearer，即使同时带有效 X-Vigil-User-ID 也拒绝。
//     避免攻击者用伪造的高优凭证降级到可伪造的低优凭证。
//   - X-Vigil-User-ID 头可被任意客户端伪造，生产环境必须禁用回退
//     （headerFallback=false，见 SEC-02）。
package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// IdentityResolver 身份解析聚合器。任一凭证源为 nil 则跳过对应轨。
//
// headerFallback 控制 X-Vigil-User-ID 头回退是否启用（SEC-02）：
//   - 本地开发/测试：true，便于 curl 调试与 e2e 注入身份
//   - 生产环境：false，彻底禁用——该头可被任意客户端伪造，
//     生产仅承认 JWT/API Key 两条强凭证链路
type IdentityResolver struct {
	jwtSigner      *JWTSigner
	apiKey         *APIKeyVerifier
	headerFallback bool
}

// NewIdentityResolver 构造聚合解析器。jwt/apiKey 任一为 nil 则跳过对应轨。
// headerFallback 为 false 时禁用 X-Vigil-User-ID 头回退（生产环境必须传 false）。
func NewIdentityResolver(jwt *JWTSigner, apiKey *APIKeyVerifier, headerFallback bool) *IdentityResolver {
	return &IdentityResolver{jwtSigner: jwt, apiKey: apiKey, headerFallback: headerFallback}
}

// Resolve 解析请求的用户 ID。返回 (uid, ok)。
// ok=false 表示无有效身份（或凭证存在但无效）。
func (r *IdentityResolver) Resolve(ctx context.Context, header http.Header) (int, bool) {
	if r == nil {
		// nil receiver 退化（仅用于未装配的测试桩）：仍受 headerFallback 约束不可能表达，
		// 故保持旧行为（允许头回退）以兼容既有 nil 用法；生产代码不会进此分支。
		return resolveHeaderUserID(header)
	}

	// 1. JWT 分支
	if r.jwtSigner != nil {
		if raw := header.Get("Authorization"); raw != "" {
			if len(raw) > 7 && strings.EqualFold(raw[:7], "Bearer ") {
				claims, err := r.jwtSigner.ParseToken(strings.TrimSpace(raw[7:]))
				if err == nil && claims.TokenType == TokenTypeAccess {
					return claims.UserID, true
				}
				// JWT 存在但无效：不回退（防伪造降级）
				return 0, false
			}
		}
	}

	// 2. API Key 分支
	if r.apiKey != nil {
		if key := header.Get("X-Vigil-Key"); key != "" {
			uid, ok := r.apiKey.Verify(ctx, key)
			if ok {
				return uid, true
			}
			// Key 存在但无效：不回退
			return 0, false
		}
	}

	// 3. 回退 X-Vigil-User-ID（仅开发/测试，生产由 headerFallback=false 拒绝）
	if !r.headerFallback {
		return 0, false
	}
	return resolveHeaderUserID(header)
}

// resolveHeaderUserID 仅解析 X-Vigil-User-ID 头（降级链路）。
func resolveHeaderUserID(header http.Header) (int, bool) {
	uidStr := header.Get("X-Vigil-User-ID")
	if uidStr == "" {
		return 0, false
	}
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		return 0, false
	}
	return uid, true
}
