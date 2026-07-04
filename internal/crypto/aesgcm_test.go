package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// genKeyB64 生成一个随机 32 字节密钥的 base64 编码（测试用）。
func genKeyB64(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// TestEncryptDecrypt_RoundTrip 加密后能原样解密。
func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	c, err := NewCipher(genKeyB64(t))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	plain := "jenkins-api-token-xyz-秘密"
	ct, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// 密文不含明文（不是简单编码）。
	if strings.Contains(ct, plain) {
		t.Fatalf("ciphertext leaks plaintext: %q", ct)
	}
	got, err := c.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

// TestEncrypt_NonceRandomized 同一明文两次加密产出不同密文（随机 nonce）。
func TestEncrypt_NonceRandomized(t *testing.T) {
	c, _ := NewCipher(genKeyB64(t))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatalf("nonce not randomized: two encryptions produced identical ciphertext")
	}
}

// TestDecrypt_WrongKey 用不同密钥解密失败（GCM tag 校验），返回泛化错误不泄露原因。
func TestDecrypt_WrongKey(t *testing.T) {
	c1, _ := NewCipher(genKeyB64(t))
	c2, _ := NewCipher(genKeyB64(t))
	ct, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(ct); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt with wrong key, got %v", err)
	}
}

// TestDecrypt_Tampered 密文被篡改后解密失败（AEAD 完整性）。
func TestDecrypt_Tampered(t *testing.T) {
	c, _ := NewCipher(genKeyB64(t))
	ct, _ := c.Encrypt("secret")
	raw, _ := base64.StdEncoding.DecodeString(ct)
	raw[len(raw)-1] ^= 0xff // 翻转最后一字节（tag 区）
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt on tampered ciphertext, got %v", err)
	}
}

// TestDecrypt_Garbage 非法 base64 / 过短密文返回 ErrDecrypt（不 panic）。
func TestDecrypt_Garbage(t *testing.T) {
	c, _ := NewCipher(genKeyB64(t))
	for _, bad := range []string{"not-base64!!!", "", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := c.Decrypt(bad); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("expected ErrDecrypt for %q, got %v", bad, err)
		}
	}
}

// TestNewCipher_KeyNotConfigured 空密钥返回 ErrKeyNotConfigured（供装配层降级）。
func TestNewCipher_KeyNotConfigured(t *testing.T) {
	if _, err := NewCipher(""); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("expected ErrKeyNotConfigured for empty key, got %v", err)
	}
}

// TestNewCipher_InvalidKey 非 32 字节 / 非法编码返回 ErrInvalidKey。
func TestNewCipher_InvalidKey(t *testing.T) {
	cases := []string{
		"tooshort", // 非编码
		base64.StdEncoding.EncodeToString([]byte("1")), // base64 但仅 1 字节
		hex.EncodeToString(make([]byte, 16)),           // 16 字节 hex（AES-128 长度，非 256）
	}
	for _, k := range cases {
		if _, err := NewCipher(k); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("expected ErrInvalidKey for %q, got %v", k, err)
		}
	}
}

// TestParseKey_HexAndBase64 hex 与 base64 编码的 32 字节密钥都能解析。
func TestParseKey_HexAndBase64(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	for _, enc := range []string{base64.StdEncoding.EncodeToString(raw), hex.EncodeToString(raw)} {
		k, err := ParseKey(enc)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", enc, err)
		}
		if len(k) != 32 {
			t.Fatalf("ParseKey length = %d, want 32", len(k))
		}
	}
}
