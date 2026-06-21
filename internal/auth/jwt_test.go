package auth

import (
	"strings"
	"testing"
	"time"
)

// newTestSigner 测试用签发器（固定 secret + 短 TTL，便于测过期）。
func newTestSigner(accessTTL, refreshTTL time.Duration) *JWTSigner {
	// HMAC-SHA256 密钥无最小长度要求，32 字节足够测试。
	return NewJWTSigner("test-secret-32bytes-long-enough!", accessTTL, refreshTTL)
}

func TestJWT_AccessRoundTrip(t *testing.T) {
	s := newTestSigner(time.Minute, time.Hour)
	tok, err := s.GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	claims, err := s.ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want alice", claims.Username)
	}
	if claims.TokenType != TokenTypeAccess {
		t.Errorf("TokenType = %q, want access", claims.TokenType)
	}
}

func TestJWT_RefreshRoundTrip(t *testing.T) {
	s := newTestSigner(time.Minute, time.Hour)
	tok, err := s.GenerateRefreshToken(7)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	claims, err := s.ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != 7 {
		t.Errorf("UserID = %d, want 7", claims.UserID)
	}
	if claims.TokenType != TokenTypeRefresh {
		t.Errorf("TokenType = %q, want refresh", claims.TokenType)
	}
}

func TestJWT_ExpiredRejected(t *testing.T) {
	// TTL 1ms，签发后睡 5ms 确保过期
	s := newTestSigner(time.Millisecond, time.Millisecond)
	tok, err := s.GenerateAccessToken(1, "bob")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := s.ParseToken(tok); err == nil {
		t.Error("expired token accepted, want error")
	}
}

func TestJWT_TamperedRejected(t *testing.T) {
	s := newTestSigner(time.Minute, time.Hour)
	tok, err := s.GenerateAccessToken(1, "bob")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	// 篡改签名最后一段（base64url 字符替换）
	tampered := tok[:len(tok)-2] + "XX"
	if _, err := s.ParseToken(tampered); err == nil {
		t.Error("tampered token accepted, want error")
	}
}

func TestJWT_EmptySecretFails(t *testing.T) {
	s := NewJWTSigner("", time.Minute, time.Hour)
	if _, err := s.GenerateAccessToken(1, "x"); err == nil {
		t.Error("GenerateAccessToken with empty secret succeeded, want error")
	}
	if _, err := s.ParseToken("any"); err == nil {
		t.Error("ParseToken with empty secret succeeded, want error")
	}
	if s.Available() {
		t.Error("Available() = true for empty secret, want false")
	}
}

func TestJWT_RefreshCannotBeUsedAsAccess(t *testing.T) {
	// 设计约束：refresh 的 token_type 不能当 access 用（虽 token 本身合法）。
	// 中间件/handler 通过 claims.TokenType == TokenTypeAccess 判断，此处仅验证类型字段可区分。
	s := newTestSigner(time.Minute, time.Hour)
	refresh, _ := s.GenerateRefreshToken(1)
	claims, err := s.ParseToken(refresh)
	if err != nil {
		t.Fatalf("ParseToken refresh: %v", err)
	}
	if claims.TokenType == TokenTypeAccess {
		t.Error("refresh token has TokenType=access, must be refresh")
	}
}

func TestJWT_RejectsAlgNone(t *testing.T) {
	// 构造一个 alg=none 的 token，确保被拒绝（防降级攻击）。
	// 手工拼装：header {"alg":"none"} + payload，签名段空。
	noneToken := "eyJhbGciOiJub25lIn0." +
		"eyJ1aWQiOjEsInVzciI6ImFsaWNlIiwidHlwIjoiYWNjZXNzIn0."
	s := newTestSigner(time.Minute, time.Hour)
	if _, err := s.ParseToken(noneToken); err == nil {
		t.Error("alg=none token accepted, want error")
	}
}

func TestJWT_DifferentSignersRejectCrossToken(t *testing.T) {
	// A 签发的 token 不能被 B 的密钥校验通过（密钥隔离）。
	a := NewJWTSigner("secret-a-xxxxxxxxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	b := NewJWTSigner("secret-b-xxxxxxxxxxxxxxxxxxxxxx", time.Minute, time.Hour)
	tok, _ := a.GenerateAccessToken(1, "x")
	if _, err := b.ParseToken(tok); err == nil {
		t.Error("token signed by A accepted by B, want error")
	}
}

// 确保 token 是三段式（header.payload.signature），符合 JWT 格式预期。
func TestJWT_TokenFormat(t *testing.T) {
	s := newTestSigner(time.Minute, time.Hour)
	tok, _ := s.GenerateAccessToken(1, "x")
	if parts := strings.Split(tok, "."); len(parts) != 3 {
		t.Errorf("token has %d parts, want 3", len(parts))
	}
}
