package auth

import (
	"strings"
	"testing"
)

func TestPassword_VerifySuccess(t *testing.T) {
	hash := HashPassword("s3cret-pw")
	if hash == "" {
		t.Fatal("HashPassword returned empty")
	}
	if !VerifyPassword("s3cret-pw", hash) {
		t.Error("VerifyPassword correct password failed")
	}
}

func TestPassword_VerifyWrongPassword(t *testing.T) {
	hash := HashPassword("s3cret-pw")
	if VerifyPassword("wrong-pw", hash) {
		t.Error("VerifyPassword wrong password succeeded, want false")
	}
}

func TestPassword_EmptyHashRejected(t *testing.T) {
	// 未设密码的用户（hash="")必须拒绝，避免"无密码即放行"绕过。
	if VerifyPassword("anything", "") {
		t.Error("VerifyPassword empty hash accepted, want false")
	}
}

func TestPassword_SaltRandomized(t *testing.T) {
	// bcrypt 每次哈希带随机盐，相同明文应产生不同哈希（防彩虹表）。
	h1 := HashPassword("same-pw")
	h2 := HashPassword("same-pw")
	if h1 == h2 {
		t.Error("two hashes of same password are identical, want different salts")
	}
	// 但两个哈希都应能校验通过
	if !VerifyPassword("same-pw", h1) || !VerifyPassword("same-pw", h2) {
		t.Error("salted hashes failed verification")
	}
}

func TestPassword_HashDoesNotLeakPlaintext(t *testing.T) {
	pw := "my-super-secret-12345"
	hash := HashPassword(pw)
	if strings.Contains(hash, pw) {
		t.Error("hash contains plaintext password")
	}
}
