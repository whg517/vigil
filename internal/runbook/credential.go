// credential.go Runbook 执行器凭据加密托管（T6.3 / 审计 S16）。
//
// 背景：此前 Runbook step 只能把 Ansible/Jenkins token 明文写进 endpoint/params，
// 泄露风险高（明文入库、可能随 step 序列化进日志/时间线）。本轮引入独立托管的
// Credential 实体（AES-256-GCM 密文存储），step 经 target.credential_ref 引用凭据 id，
// 执行器在**执行时**解密凭据并注入 Authorization/自定义头。
//
// ★ 安全红线（明文不泄露）：
//   - 明文只在解密后到设置 HTTP 头这一瞬间存在于内存，随请求发出即释放。
//   - 绝不把明文写进 step、params、日志、时间线、错误信息或 API 响应。
//   - 解密失败只返回泛化错误（不含密文/密钥/明文）。
package runbook

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/crypto"
)

// injectedHeader 解密后待注入的 HTTP 头（明文，仅内存短暂持有）。
type injectedHeader struct {
	name  string // 头名（Authorization 或自定义头）
	value string // 头值明文（含 scheme 前缀，如 "Bearer xxx"）
}

// CredentialResolver 把凭据 id 解析为待注入的 HTTP 头（解密在此完成）。
//
// 执行器持有它（可选）：target.credential_ref>0 时调用注入凭据；resolver 为 nil 或
// 未配加密密钥时降级为不注入（凭据托管未启用，step 仍可执行只是不带凭据）。
type CredentialResolver interface {
	// ResolveHeader 反查凭据、解密、按 type 组装成 HTTP 头。
	// credID<=0 返回 (nil,nil)（无凭据引用，不注入）。
	// 凭据不存在/解密失败返回 error（调用方按执行失败处理，但错误信息不含明文）。
	ResolveHeader(ctx context.Context, credID int) (*injectedHeader, error)
}

// EntCredentialResolver 基于 ent + crypto.Cipher 的凭据解析器实现。
type EntCredentialResolver struct {
	db     *ent.Client
	cipher *crypto.Cipher
}

// NewEntCredentialResolver 创建凭据解析器。cipher 为 nil 时 ResolveHeader 对任何
// credID>0 返回错误（托管未启用却引用了凭据 = 配置矛盾，须显式失败而非静默放行明文）。
func NewEntCredentialResolver(db *ent.Client, cipher *crypto.Cipher) *EntCredentialResolver {
	return &EntCredentialResolver{db: db, cipher: cipher}
}

// ResolveHeader 反查凭据 → 解密 → 按 type 组装头。
func (r *EntCredentialResolver) ResolveHeader(ctx context.Context, credID int) (*injectedHeader, error) {
	if credID <= 0 {
		return nil, nil // 无引用：不注入
	}
	if r == nil || r.db == nil {
		return nil, nil // 未装配：降级不注入（渐进/单测）
	}
	if r.cipher == nil {
		// 引用了凭据但托管未启用（无密钥）：显式失败，不放行（明文兜底本就不存在）。
		return nil, fmt.Errorf("credential ref %d requires encryption key (VIGIL_CREDENTIAL_ENCRYPTION_KEY) not configured", credID)
	}
	cred, err := r.db.Credential.Get(ctx, credID)
	if err != nil {
		return nil, fmt.Errorf("credential ref %d not found", credID) // 不含明文
	}
	plaintext, err := r.cipher.Decrypt(cred.SecretCiphertext)
	if err != nil {
		// 解密失败：泛化错误，不含密文/密钥/明文（crypto.ErrDecrypt 已脱敏）。
		return nil, fmt.Errorf("decrypt credential ref %d: %w", credID, err)
	}
	return headerFor(cred.Type.String(), plaintext, cred.Config), nil
}

// headerFor 按凭据 type 组装 HTTP 头（明文只在返回值里短暂持有）。
//
//	bearer → Authorization: Bearer <secret>
//	basic  → Authorization: Basic <secret>（secret 应为 base64(user:pass)）
//	token  → Authorization: <secret>（原样，含用户自带 scheme 前缀）
//	header → <config.header>: <secret>（自定义头，头名缺省回退 Authorization）
func headerFor(credType, secret string, cfg map[string]any) *injectedHeader {
	switch credType {
	case "basic":
		return &injectedHeader{name: "Authorization", value: "Basic " + secret}
	case "token":
		return &injectedHeader{name: "Authorization", value: secret}
	case "header":
		name := "Authorization"
		if cfg != nil {
			if h, ok := cfg["header"].(string); ok && h != "" {
				name = h
			}
		}
		return &injectedHeader{name: name, value: secret}
	default: // bearer
		return &injectedHeader{name: "Authorization", value: "Bearer " + secret}
	}
}
