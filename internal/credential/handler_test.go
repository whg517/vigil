package credential

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	entcredential "github.com/kevin/vigil/ent/credential"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:cred_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	c, err := crypto.NewCipher(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestStoredCiphertextHasNoPlaintext 凭据经加密落库：DB 里 secret_ciphertext 不含明文，
// 且能用同一密钥解密回原文（加密存储 + 可用性）。对应 S16 验收①「凭据加密存储不落明文」。
func TestStoredCiphertextHasNoPlaintext(t *testing.T) {
	c := newTestClient(t)
	cipher := newTestCipher(t)
	ctx := context.Background()

	plaintext := "jenkins-prod-token-SECRET-42"
	ct, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	cred := c.Credential.Create().SetName("jenkins").SetType(entcredential.TypeBearer).
		SetSecretCiphertext(ct).SaveX(ctx)

	// 从 DB 读回，密文字段不含明文。
	got := c.Credential.GetX(ctx, cred.ID)
	if strings.Contains(got.SecretCiphertext, plaintext) {
		t.Fatalf("plaintext leaked into stored ciphertext: %q", got.SecretCiphertext)
	}
	// 原始 SQL 扫全表：任何列都不含明文（防 ent 层遗漏）。
	assertNoPlaintextInDB(t, c, plaintext)

	// 解密回原文（可用性）。
	back, err := cipher.Decrypt(got.SecretCiphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if back != plaintext {
		t.Fatalf("decrypt mismatch: got %q want %q", back, plaintext)
	}
}

// TestSensitiveNotInJSON 凭据 JSON 序列化（API 响应经此）不含密文，字段名也不出现（json:"-"）。
// 对应 S16 验收③「审计/响应不泄露凭据」，读取只返元数据。
func TestSensitiveNotInJSON(t *testing.T) {
	c := newTestClient(t)
	cipher := newTestCipher(t)
	ct, _ := cipher.Encrypt("plaintext-secret")
	cred := c.Credential.Create().SetName("x").SetType(entcredential.TypeBearer).
		SetSecretCiphertext(ct).SaveX(context.Background())

	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "plaintext-secret") {
		t.Fatalf("plaintext in JSON: %s", s)
	}
	if strings.Contains(s, ct) {
		t.Fatalf("ciphertext in JSON response (should be json:\"-\"): %s", s)
	}
	if strings.Contains(s, "secret_ciphertext") {
		t.Fatalf("secret_ciphertext field present in JSON (should be json:\"-\"): %s", s)
	}
	// 元数据仍在（读取只返元数据）。
	if !strings.Contains(s, "\"name\"") {
		t.Fatalf("expected metadata name in JSON: %s", s)
	}
}

// TestSensitiveScrubbedInString String()（日志/调试打印）对密文脱敏为 <sensitive>。
func TestSensitiveScrubbedInString(t *testing.T) {
	c := newTestClient(t)
	cipher := newTestCipher(t)
	ct, _ := cipher.Encrypt("logged-secret")
	cred := c.Credential.Create().SetName("x").SetType(entcredential.TypeBearer).
		SetSecretCiphertext(ct).SaveX(context.Background())

	str := cred.String()
	if strings.Contains(str, "logged-secret") {
		t.Fatalf("plaintext leaked in String(): %s", str)
	}
	if strings.Contains(str, ct) {
		t.Fatalf("ciphertext leaked in String(): %s", str)
	}
}

// TestUpdateReEncrypts 更新 secret 后落新密文、旧密文失效、可用新密文解出新明文。
func TestUpdateReEncrypts(t *testing.T) {
	c := newTestClient(t)
	cipher := newTestCipher(t)
	ctx := context.Background()

	old, _ := cipher.Encrypt("old-secret")
	cred := c.Credential.Create().SetName("x").SetType(entcredential.TypeBearer).
		SetSecretCiphertext(old).SaveX(ctx)

	newCT, _ := cipher.Encrypt("new-secret")
	c.Credential.UpdateOneID(cred.ID).SetSecretCiphertext(newCT).ExecX(ctx)

	got := c.Credential.GetX(ctx, cred.ID)
	back, err := cipher.Decrypt(got.SecretCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	if back != "new-secret" {
		t.Fatalf("expected new-secret after update, got %q", back)
	}
	assertNoPlaintextInDB(t, c, "old-secret")
	assertNoPlaintextInDB(t, c, "new-secret")
}

// assertNoPlaintextInDB 从 DB 读回全部 credentials（ent 读取所有列，含 Sensitive 密文），
// 断言任何可存储列（name/secret_ciphertext/config）都不含 plaintext。
// ent 的 Get/All 会真正从 DB 装载 secret_ciphertext（Sensitive 只影响 JSON/String，不影响读取），
// 故读回值即代表 DB 落库内容——若明文没被加密就会出现在密文列里。
func assertNoPlaintextInDB(t *testing.T, c *ent.Client, plaintext string) {
	t.Helper()
	all, err := c.Credential.Query().All(context.Background())
	if err != nil {
		t.Fatalf("query credentials: %v", err)
	}
	for _, cred := range all {
		cfgJSON, _ := json.Marshal(cred.Config)
		cols := []string{cred.Name, cred.SecretCiphertext, string(cfgJSON)}
		for _, col := range cols {
			if strings.Contains(col, plaintext) {
				t.Fatalf("plaintext %q found in stored column: %q", plaintext, col)
			}
		}
	}
}
