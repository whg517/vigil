// Package crypto 提供 Vigil 的统一对称加密能力（T6.3）。
//
// 用途：托管外接系统的敏感凭据（Runbook 执行器的 Ansible/Jenkins token、
// 工单集成的 API token 等），使其在数据库中以密文存储、不落明文。
//
// 设计取舍（对应 docs/capabilities/06-runbook.md §7 Q1「加密存储于 Vigil」）：
//   - 算法：AES-256-GCM（AEAD，同时保证机密性与完整性，抗篡改）。
//     与 IM 回调用的 AES-256-CBC 不同——CBC 无完整性校验且需外部管填充，
//     托管凭据用 GCM 更稳妥（业界推荐的对称 AEAD）。
//   - 密钥：仅从环境变量注入（VIGIL_CREDENTIAL_ENCRYPTION_KEY），绝不硬编码/提交 git。
//     密钥为 32 字节（AES-256），以 base64 或 hex 编码传入（见 ParseKey）。
//   - 密文格式：base64( nonce(12B) || ciphertext || tag )，自包含随机 nonce，
//     同一明文每次加密产出不同密文（GCM nonce 唯一性由 crypto/rand 保证）。
//   - 本包是**唯一**的凭据加密实现（统一机制）：Runbook 凭据与工单集成凭据都复用它，
//     避免项目里出现两套加密。
//
// ★ 安全红线：明文只在进程内存中短暂存在（加密前/解密后立即使用），
// 绝不写入日志、时间线、错误信息或 API 响应。本包不打印任何明文/密文。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ErrKeyNotConfigured 未配置加密密钥。凭据托管功能据此降级：
// 无密钥时创建/更新凭据被拒（不允许明文兜底落库），而非静默存明文。
var ErrKeyNotConfigured = errors.New("credential encryption key not configured")

// ErrInvalidKey 密钥格式非法（非 32 字节，或 base64/hex 解码失败）。
var ErrInvalidKey = errors.New("invalid credential encryption key: must be 32 bytes (AES-256), base64 or hex encoded")

// ErrDecrypt 解密失败（密文损坏/被篡改，或密钥不匹配）。GCM 会在 tag 校验失败时返回，
// 不区分具体原因以免侧信道泄露。
var ErrDecrypt = errors.New("credential decryption failed (ciphertext corrupt or wrong key)")

// keySize AES-256 密钥字节数。
const keySize = 32

// Cipher 基于 AES-256-GCM 的凭据加解密器。
//
// 零值不可用，须用 NewCipher 构造。未配置密钥时 NewCipher 返回 ErrKeyNotConfigured，
// 装配层据此把凭据托管标记为「未启用」（Enabled()==false）。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 用 base64/hex 编码的 32 字节密钥构造加解密器。
//
// rawKey 为空返回 ErrKeyNotConfigured（供装配层降级判断）；
// 格式非法返回 ErrInvalidKey。
func NewCipher(rawKey string) (*Cipher, error) {
	if rawKey == "" {
		return nil, ErrKeyNotConfigured
	}
	key, err := ParseKey(rawKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// ParseKey 把 base64 或 hex 编码的密钥解析为 32 字节原始密钥。
//
// 先尝试 base64（标准/URL 两种字母表），再尝试 hex；均须解出恰好 32 字节。
// 便于运维用 `openssl rand -base64 32` 或 `openssl rand -hex 32` 生成密钥。
func ParseKey(rawKey string) ([]byte, error) {
	// 尝试标准 base64（rand -base64 32 输出 44 字符含 '='）。
	if b, err := base64.StdEncoding.DecodeString(rawKey); err == nil && len(b) == keySize {
		return b, nil
	}
	// 尝试 URL-safe base64（无填充变体也覆盖）。
	if b, err := base64.RawStdEncoding.DecodeString(rawKey); err == nil && len(b) == keySize {
		return b, nil
	}
	// 尝试 hex（rand -hex 32 输出 64 字符）。
	if b, err := hex.DecodeString(rawKey); err == nil && len(b) == keySize {
		return b, nil
	}
	return nil, ErrInvalidKey
}

// Encrypt 加密明文，返回 base64( nonce || ciphertext || tag )。
//
// 每次调用生成新的随机 nonce（GCM 要求 nonce 在同一密钥下唯一，crypto/rand 保证）。
// 空明文也照常加密（返回非空密文）——调用方通常在明文为空时跳过存储，本包不特判。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	// Seal 把 nonce 作为前缀（dst=nonce），密文追加其后，一次输出自包含结构。
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 Encrypt 产出的 base64 密文，返回明文。
//
// 密文损坏/被篡改/密钥不匹配时返回 ErrDecrypt（GCM tag 校验失败），
// 不泄露具体失败原因（避免 padding-oracle 式侧信道）。
func (c *Cipher) Decrypt(ciphertextB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", ErrDecrypt
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrDecrypt
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}
