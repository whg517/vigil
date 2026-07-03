package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

func TestGenerateAPIKey_Format(t *testing.T) {
	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(plaintext, "vgl_") {
		t.Errorf("plaintext missing vgl_ prefix: %s", plaintext)
	}
	// vgl_ + 32 hex = 36 字符
	if len(plaintext) != 4+32 {
		t.Errorf("plaintext length=%d, want 36", len(plaintext))
	}
	if hash == "" {
		t.Error("hash empty")
	}
	// hash 是 SHA256 的 hex（64 字符）
	if len(hash) != 64 {
		t.Errorf("hash length=%d, want 64", len(hash))
	}
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	// 两次生成应不同（随机性）
	p1, _, _ := GenerateAPIKey()
	p2, _, _ := GenerateAPIKey()
	if p1 == p2 {
		t.Error("two generated keys are identical, want random")
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	// 相同明文哈希一致（两次调用结果应相同）
	p := "vgl_abc123"
	h1, h2 := HashToken(p), HashToken(p)
	if h1 != h2 {
		t.Error("HashToken not deterministic for same input")
	}
	// 不同明文哈希不同
	if HashToken("vgl_a") == HashToken("vgl_b") {
		t.Error("different inputs produced same hash")
	}
}

func TestTokenPrefix(t *testing.T) {
	p := "vgl_a1b2c3d4e5f6"
	if got := TokenPrefix(p); got != "vgl_a1b2c3d4" {
		t.Errorf("TokenPrefix=%q, want vgl_a1b2c3d4", got)
	}
	// 短串原样返回
	short := "vgl"
	if got := TokenPrefix(short); got != short {
		t.Errorf("TokenPrefix short=%q, want %q", got, short)
	}
}

func TestAPIKeyVerifier_Valid(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_verify?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("u").SetEmail("u@v.local").Save(ctx)
	plaintext, hash, _ := GenerateAPIKey()
	_, _ = c.APIKey.Create().
		SetName("k").SetTokenHash(hash).SetPrefix(TokenPrefix(plaintext)).SetUserID(u.ID).
		Save(ctx)

	v := NewAPIKeyVerifier(c)
	uid, ok := v.Verify(ctx, plaintext)
	if !ok {
		t.Error("valid key rejected")
	}
	if uid != u.ID {
		t.Errorf("uid=%d, want %d", uid, u.ID)
	}
	// last_used_at 应被更新
	k, _ := c.APIKey.Query().First(ctx)
	if k.LastUsedAt == nil {
		t.Error("last_used_at not updated after verify")
	}
}

func TestAPIKeyVerifier_WrongKey(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_wrong?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("u").SetEmail("u@v.local").Save(ctx)
	_, hash, _ := GenerateAPIKey()
	_, _ = c.APIKey.Create().SetName("k").SetTokenHash(hash).SetPrefix("vgl_x").SetUserID(u.ID).Save(ctx)

	v := NewAPIKeyVerifier(c)
	if _, ok := v.Verify(ctx, "vgl_wrongkey"); ok {
		t.Error("wrong key accepted, want rejected")
	}
}

func TestAPIKeyVerifier_Disabled(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_disabled?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("u").SetEmail("u@v.local").Save(ctx)
	plaintext, hash, _ := GenerateAPIKey()
	_, _ = c.APIKey.Create().
		SetName("k").SetTokenHash(hash).SetPrefix(TokenPrefix(plaintext)).
		SetUserID(u.ID).SetStatus("disabled").Save(ctx)

	v := NewAPIKeyVerifier(c)
	if _, ok := v.Verify(ctx, plaintext); ok {
		t.Error("disabled key accepted, want rejected")
	}
}

// TestAPIKeyVerifier_DisabledOwner 归属用户被禁用 → Key 失效（安全审计 S4）。
// Key 自身 status=active、未过期，但归属 User.status=disabled，应拒绝，
// 杜绝禁用用户名下的 API Key 永久可用旁路鉴权。
func TestAPIKeyVerifier_DisabledOwner(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_disabled_owner?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("u").SetEmail("u@v.local").SetStatus("disabled").Save(ctx)
	plaintext, hash, _ := GenerateAPIKey()
	_, _ = c.APIKey.Create().
		SetName("k").SetTokenHash(hash).SetPrefix(TokenPrefix(plaintext)).
		SetUserID(u.ID).Save(ctx) // Key 本身 active、不过期

	v := NewAPIKeyVerifier(c)
	if _, ok := v.Verify(ctx, plaintext); ok {
		t.Error("key of disabled owner accepted, want rejected")
	}
}

func TestAPIKeyVerifier_Expired(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_expired?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("u").SetEmail("u@v.local").Save(ctx)
	plaintext, hash, _ := GenerateAPIKey()
	past := time.Now().Add(-1 * time.Hour)
	_, _ = c.APIKey.Create().
		SetName("k").SetTokenHash(hash).SetPrefix(TokenPrefix(plaintext)).
		SetUserID(u.ID).SetExpiresAt(past).Save(ctx)

	v := NewAPIKeyVerifier(c)
	if _, ok := v.Verify(ctx, plaintext); ok {
		t.Error("expired key accepted, want rejected")
	}
}

func TestAPIKeyVerifier_Empty(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:apikey_empty?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	v := NewAPIKeyVerifier(c)
	if _, ok := v.Verify(context.Background(), ""); ok {
		t.Error("empty key accepted, want rejected")
	}
}
