// resolver.go 身份解析聚合器（能力域 13）。
//
// 统一 JWT / API Key / X-Vigil-User-ID 三轨身份解析，供中间件使用。
// 替代给中间件函数逐个传 signer/verifier 的参数膨胀方案。
//
// 解析顺序（优先级）：
//  1. Authorization: Bearer <jwt>   —— JWT 登录态（人，Web/IM）
//  2. X-Vigil-Key: <apikey>         —— API Key（程序化接入）
//  3. X-Vigil-User-ID: <uid>        —— 降级兼容（默认关闭，须 VIGIL_AUTH_HEADER_FALLBACK 显式开启）
//
// 安全约束（关键）：
//   - 任一凭证"存在但无效"时，不回退到更低优先级的凭证。
//     例：带了无效 Bearer，即使同时带有效 X-Vigil-User-ID 也拒绝。
//     避免攻击者用伪造的高优凭证降级到可伪造的低优凭证。
//   - X-Vigil-User-ID 头可被任意客户端伪造，回退默认关闭（headerFallback=false），
//     仅本地开发显式开启；生产环境无条件强制关闭（SEC-02 修订，
//     见 config.Auth.EffectiveHeaderFallback）。
package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/user"
)

// IdentityResolver 身份解析聚合器。任一凭证源为 nil 则跳过对应轨。
//
// headerFallback 控制 X-Vigil-User-ID 头回退是否启用（SEC-02 修订）：
//   - 默认 false（含本地开发）——该头可被任意客户端伪造，危险行为须显式开启；
//   - 本地开发调试可经 VIGIL_AUTH_HEADER_FALLBACK=true 显式开启（便于 curl 注入身份）；
//   - 生产环境无条件 false（配置层强制），生产仅承认 JWT/API Key 两条强凭证链路
type IdentityResolver struct {
	jwtSigner      *JWTSigner
	apiKey         *APIKeyVerifier
	headerFallback bool
	// db 用于 JWT 令牌吊销校验（T0.4）：解析出 claims 后查用户当前 token_version 比对。
	// 为 nil 时跳过吊销校验（退化为纯无状态校验，仅测试桩/未装配场景）。
	db *ent.Client
}

// NewIdentityResolver 构造聚合解析器。jwt/apiKey 任一为 nil 则跳过对应轨。
// headerFallback 为 false 时禁用 X-Vigil-User-ID 头回退（默认应为 false，
// 装配层经 config.Auth.EffectiveHeaderFallback 决定，生产强制 false）。
// db 用于 JWT 令牌吊销校验（改密后旧 token 失效，T0.4）；为 nil 时跳过该校验。
//
// 性能权衡（T0.4）：启用 db 后每次 JWT 鉴权多一次按主键查 User 的查询。
// 取舍——安全优先：改密（token_version 自增）须让旧 token 立即失效，无状态 JWT 做不到主动吊销，
// 只能在校验时回查库比对版本号。单主键查询开销可控（有主键索引），后续可加 Redis 缓存
// token_version（改密时失效缓存）把常态查询挪到内存。
func NewIdentityResolver(jwt *JWTSigner, apiKey *APIKeyVerifier, headerFallback bool, db *ent.Client) *IdentityResolver {
	return &IdentityResolver{jwtSigner: jwt, apiKey: apiKey, headerFallback: headerFallback, db: db}
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
				if err == nil && claims.TokenType == TokenTypeAccess && r.tokenVersionValid(ctx, claims) {
					return claims.UserID, true
				}
				// JWT 存在但无效（签名/过期/类型不符/已吊销）：不回退（防伪造降级）
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

	// 3. 回退 X-Vigil-User-ID（默认 headerFallback=false 拒绝，须显式开启且仅限非生产）
	if !r.headerFallback {
		return 0, false
	}
	return resolveHeaderUserID(header)
}

// tokenVersionValid 校验 access token 是否被改密吊销（T0.4）。
// 查用户当前 token_version 与 claims 快照比对：不一致 = 改密后签发过新版本 = 旧 token 作废。
// db 为 nil 时跳过校验（无状态回退，仅测试桩/未装配场景）；用户不存在同样视为无效（拒绝）。
func (r *IdentityResolver) tokenVersionValid(ctx context.Context, claims *Claims) bool {
	if r.db == nil {
		return true
	}
	// 只取 token_version 一列，避免拉全字段（含 password_hash 等）。
	cur, err := r.db.User.Query().
		Where(user.IDEQ(claims.UserID)).
		Select(user.FieldTokenVersion).
		Int(ctx)
	if err != nil {
		return false
	}
	return cur == claims.TokenVersion
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
