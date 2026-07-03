// apikey.go API Key 签发与校验（能力域 13 §API Key 管理，PRD M13.7）。
//
// 与 JWT 并列的第二种凭证：用于程序化接入（开放 API、CI/CD、外部系统调 Vigil）。
//
// 安全设计：
//   - 明文 token 仅创建时返回一次，库内只存 SHA256(token_hash)
//   - 明文格式 vgl_<32位随机>，prefix 存前 12 字符供列表识别
//   - 校验时查库（与 JWT 无状态不同，因需支持撤销/过期/last_used_at 更新）
//   - 鉴权继承归属 User 的角色（scope 字段预留，本期不强制收敛）
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/apikey"
	"github.com/kevin/vigil/ent/user"
)

// apikeyPrefix 明文 token 的可识别前缀（防与其他系统 token 混淆）。
const apikeyPrefix = "vgl_"

// tokenRandomLen 明文随机部分长度（字节，hex 编码后翻倍）。
const tokenRandomLen = 16 // 16 字节 → 32 hex 字符

// APIKeyVerifier API Key 校验器。校验时查库（非无状态），更新 last_used_at。
type APIKeyVerifier struct {
	db *ent.Client
}

// NewAPIKeyVerifier 构造校验器。
func NewAPIKeyVerifier(db *ent.Client) *APIKeyVerifier {
	return &APIKeyVerifier{db: db}
}

// Generate 生成新 API Key 的明文与哈希。
// 返回 (plaintext, hash)：plaintext 仅返回给调用方一次，hash 入库。
func GenerateAPIKey() (plaintext, hash string, err error) {
	buf := make([]byte, tokenRandomLen)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate random: %w", err)
	}
	plaintext = apikeyPrefix + hex.EncodeToString(buf)
	hash = HashToken(plaintext)
	return plaintext, hash, nil
}

// HashToken 计算 token 的 SHA256 哈希（hex 编码）。入库与校验共用，保证一致。
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// TokenPrefix 取明文前缀（列表展示识别用）。
func TokenPrefix(plaintext string) string {
	if len(plaintext) < 12 {
		return plaintext
	}
	return plaintext[:12]
}

// Verify 校验 API Key 并返回归属 user_id。
// 校验：token_hash 匹配 + status=active + 未过期。成功时更新 last_used_at。
// 返回 (userID, ok)：任一校验失败返回 (0, false)，不区分原因（防探测）。
func (v *APIKeyVerifier) Verify(ctx context.Context, plaintext string) (int, bool) {
	if v == nil || v.db == nil || plaintext == "" {
		return 0, false
	}
	k, err := v.db.APIKey.Query().
		Where(apikey.TokenHashEQ(HashToken(plaintext))).
		Only(ctx)
	if err != nil {
		return 0, false
	}
	if k.Status != apikey.StatusActive {
		return 0, false
	}
	if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
		return 0, false
	}
	// 更新 last_used_at（失败不影响鉴权结果，best-effort）
	_ = v.db.APIKey.UpdateOneID(k.ID).SetLastUsedAt(time.Now()).Exec(ctx)
	// user_id 是 edge，需查归属用户
	u, err := k.QueryUser().Only(ctx)
	if err != nil {
		return 0, false
	}
	// 归属用户被禁用（status=disabled）则 Key 失效：API Key 默认长期有效，
	// 若不查归属 User.status，禁用用户名下的 Key 将永久可用旁路鉴权（安全审计 S4）。
	if u.Status != user.StatusActive {
		return 0, false
	}
	return u.ID, true
}
