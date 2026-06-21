// resolver.go 身份解析聚合器（能力域 13）。
//
// 统一 JWT / API Key / X-Vigil-User-ID 三轨身份解析，供中间件使用。
// 替代给中间件函数逐个传 signer/verifier 的参数膨胀方案。
//
// 解析顺序（优先级）：
//  1. Authorization: Bearer <jwt>   —— JWT 登录态（人，Web/IM）
//  2. X-Vigil-Key: <apikey>         —— API Key（程序化接入）
//  3. X-Vigil-User-ID: <uid>        —— 降级兼容（AUTH_ENABLED=false 阶段）
//
// 安全约束（关键）：
//   - 任一凭证"存在但无效"时，不回退到更低优先级的凭证。
//     例：带了无效 Bearer，即使同时带有效 X-Vigil-User-ID 也拒绝。
//     避免攻击者用伪造的高优凭证降级到可伪造的低优凭证。
package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// IdentityResolver 身份解析聚合器。任一凭证源为 nil 则跳过对应轨。
type IdentityResolver struct {
	jwtSigner *JWTSigner
	apiKey    *APIKeyVerifier
}

// NewIdentityResolver 构造聚合解析器。jwt/apiKey 任一为 nil 则跳过对应轨。
func NewIdentityResolver(jwt *JWTSigner, apiKey *APIKeyVerifier) *IdentityResolver {
	return &IdentityResolver{jwtSigner: jwt, apiKey: apiKey}
}

// Resolve 解析请求的用户 ID。返回 (uid, ok)。
// ok=false 表示无有效身份（或凭证存在但无效）。
func (r *IdentityResolver) Resolve(ctx context.Context, header http.Header) (int, bool) {
	if r == nil {
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

	// 3. 回退 X-Vigil-User-ID（兼容）
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
