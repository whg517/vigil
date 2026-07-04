package runbook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	entcredential "github.com/kevin/vigil/ent/credential"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/crypto"
)

// newTestCipher 生成随机密钥的加密器（测试用）。
func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	c, err := crypto.NewCipher(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return c
}

// createCredential 建一条加密凭据，返回其 id。
func createCredential(t *testing.T, c *ent.Client, cipher *crypto.Cipher, typ, secret string, cfg map[string]any) int {
	t.Helper()
	ct, err := cipher.Encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	b := c.Credential.Create().SetName("cred").SetType(entcredential.Type(typ)).SetSecretCiphertext(ct)
	if cfg != nil {
		b.SetConfig(cfg)
	}
	cred, err := b.Save(context.Background())
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return cred.ID
}

// TestHTTPExecutor_InjectsBearerCredential 执行时解密凭据并注入 Authorization: Bearer。
func TestHTTPExecutor_InjectsBearerCredential(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t)
	cipher := newTestCipher(t)
	credID := createCredential(t, c, cipher, "bearer", "super-secret-token", nil)

	exec := NewHTTPExecutor()
	exec.SetAllowPrivate(true) // httptest 绑 127.0.0.1
	exec.SetCredentialResolver(NewEntCredentialResolver(c, cipher))

	out, err := exec.Execute(context.Background(), schema.StepTarget{
		Kind: "http", Endpoint: srv.URL, Readonly: true, CredentialRef: credID,
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotAuth != "Bearer super-secret-token" {
		t.Fatalf("credential not injected as bearer: got %q", gotAuth)
	}
	// ★ 明文绝不出现在执行输出里。
	if strings.Contains(out, "super-secret-token") {
		t.Fatalf("plaintext credential leaked into output: %q", out)
	}
}

// TestHTTPExecutor_InjectsCustomHeaderCredential header 类型注入自定义头名。
func TestHTTPExecutor_InjectsCustomHeaderCredential(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	cipher := newTestCipher(t)
	credID := createCredential(t, c, cipher, "header", "key-abc", map[string]any{"header": "X-Api-Key"})

	exec := NewHTTPExecutor()
	exec.SetAllowPrivate(true)
	exec.SetCredentialResolver(NewEntCredentialResolver(c, cipher))

	if _, err := exec.Execute(context.Background(), schema.StepTarget{
		Kind: "http", Endpoint: srv.URL, Readonly: true, CredentialRef: credID,
	}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotHeader != "key-abc" {
		t.Fatalf("custom header credential not injected: got %q", gotHeader)
	}
}

// TestInternalExecutor_CheckHTTPInjectsCredential 诊断探活也注入凭据（访问需鉴权只读 API）。
func TestInternalExecutor_CheckHTTPInjectsCredential(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	cipher := newTestCipher(t)
	credID := createCredential(t, c, cipher, "token", "Raw scheme-value", nil) // token 原样注入

	exec := NewInternalExecutor()
	exec.SetAllowPrivate(true)
	exec.SetCredentialResolver(NewEntCredentialResolver(c, cipher))

	if _, err := exec.Execute(context.Background(), schema.StepTarget{
		Kind: "internal", Endpoint: srv.URL, Readonly: true, CredentialRef: credID,
	}, map[string]any{"action": "check_http"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotAuth != "Raw scheme-value" {
		t.Fatalf("token credential not injected raw: got %q", gotAuth)
	}
}

// TestResolveHeader_NoCipher 引用了凭据但未配加密器 → 显式失败（不放行明文兜底），错误不含明文。
func TestResolveHeader_NoCipher(t *testing.T) {
	c := newTestClient(t)
	// 无 cipher 时用一个占位 ciphertext 直接建实体（模拟历史数据）。
	cred, err := c.Credential.Create().SetName("x").SetType(entcredential.TypeBearer).
		SetSecretCiphertext("placeholder").Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := NewEntCredentialResolver(c, nil) // cipher=nil
	if _, err := r.ResolveHeader(context.Background(), cred.ID); err == nil {
		t.Fatal("expected error when cipher not configured, got nil")
	}
}

// TestResolveHeader_NoRef credID<=0 返回 (nil,nil)（无引用不注入，不报错）。
func TestResolveHeader_NoRef(t *testing.T) {
	c := newTestClient(t)
	r := NewEntCredentialResolver(c, newTestCipher(t))
	hdr, err := r.ResolveHeader(context.Background(), 0)
	if err != nil || hdr != nil {
		t.Fatalf("expected (nil,nil) for no ref, got (%v,%v)", hdr, err)
	}
}

// TestResolveHeader_DecryptFailNoLeak 密文与密钥不匹配 → 错误不含密文/明文。
func TestResolveHeader_DecryptFailNoLeak(t *testing.T) {
	c := newTestClient(t)
	cipher1 := newTestCipher(t)
	credID := createCredential(t, c, cipher1, "bearer", "the-plaintext", nil)
	// 用不同密钥的解析器解密 → 失败。
	r := NewEntCredentialResolver(c, newTestCipher(t))
	_, err := r.ResolveHeader(context.Background(), credID)
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
	if strings.Contains(err.Error(), "the-plaintext") {
		t.Fatalf("error message leaked plaintext: %v", err)
	}
}

// TestExecuteStep_CredentialErrorFailsStep 凭据注入失败使该步执行失败（不静默无鉴权发出请求）。
func TestExecuteStep_CredentialErrorFailsStep(t *testing.T) {
	// 目标服务器要求 Authorization，无则 401——但这里我们让凭据解析先失败（无 cipher）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	cred, _ := c.Credential.Create().SetName("x").SetType(entcredential.TypeBearer).
		SetSecretCiphertext("placeholder").Save(context.Background())

	exec := NewHTTPExecutor()
	exec.SetAllowPrivate(true)
	exec.SetCredentialResolver(NewEntCredentialResolver(c, nil)) // cipher=nil → 引用即失败

	_, err := exec.Execute(context.Background(), schema.StepTarget{
		Kind: "http", Endpoint: srv.URL, Readonly: true, CredentialRef: cred.ID,
	}, nil)
	if err == nil {
		t.Fatal("expected execute to fail when credential cannot be resolved, got nil")
	}
}
