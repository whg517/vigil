package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/user"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newAuthTestClient 构造内存 sqlite client + 已植入测试用户。
// alice 密码 "pw"，bob 状态 disabled。
func newAuthTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:auth_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	_, _ = c.User.Create().
		SetUsername("alice").
		SetName("Alice").
		SetEmail("alice@vigil.local").
		SetPasswordHash(HashPassword("pw")).
		Save(ctx)
	_, _ = c.User.Create().
		SetUsername("bob").
		SetEmail("bob@vigil.local").
		SetPasswordHash(HashPassword("pw")).
		SetStatus(userStatusDisabled).
		Save(ctx)
	return c
}

const userStatusDisabled = "disabled"

// postJSON 辅助：构造 POST + JSON body 请求。
func postJSON(target string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

func newTestSigner1m() *JWTSigner {
	return NewJWTSigner("handler-test-secret-xxxxxxxxxxxxxx", time.Minute, time.Hour)
}

// TestLogin_Success 正确用户名密码 → 200 + 双 token + 用户信息。
func TestLogin_Success(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "pw"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp loginResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		t.Error("tokens empty")
	}
	if resp.User.Username != "alice" {
		t.Errorf("user=%+v", resp.User)
	}
	// access token 可被 signer 校验
	claims, err := s.ParseToken(resp.AccessToken)
	if err != nil {
		t.Errorf("access token invalid: %v", err)
	}
	if claims.TokenType != TokenTypeAccess {
		t.Errorf("token type=%q, want access", claims.TokenType)
	}
}

// TestLogin_WrongPassword 错误密码 → 401，错误信息不泄露用户是否存在。
func TestLogin_WrongPassword(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "wrong"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestLogin_UnknownUser 不存在的用户 → 401（与错误密码相同信息，防枚举）。
func TestLogin_UnknownUser(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "nobody", Password: "pw"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestLogin_DisabledUser 停用用户 → 403。
func TestLogin_DisabledUser(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "bob", Password: "pw"}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
}

// TestLogin_SignerNil signer 未配置 → 500（降级保护）。
func TestLogin_SignerNil(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, nil)
	e := echo.New()
	e.POST("/api/v1/auth/login", h.login)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "pw"}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
}

// TestRefresh_ValidRefresh 有效 refresh → 200 + 新 access。
func TestRefresh_ValidRefresh(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	refresh, _ := s.GenerateRefreshToken(1, 0)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: refresh}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["access_token"] == "" {
		t.Error("access_token empty")
	}
}

// TestRefresh_DisabledUser 禁用用户的 refresh token 不能换 access → 401（安全审计 S3）。
// bob（uid 2）状态 disabled，refresh 应被拒绝，杜绝禁用用户凭旧 token 持续访问。
func TestRefresh_DisabledUser(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	bob, _ := c.User.Query().Where(user.UsernameEQ("bob")).Only(context.Background())
	refresh, _ := s.GenerateRefreshToken(bob.ID, 0)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: refresh}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRefresh_UserDeleted refresh token 指向已不存在的用户 → 401。
func TestRefresh_UserDeleted(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	refresh, _ := s.GenerateRefreshToken(9999, 0) // 无此用户
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: refresh}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRefresh_WithAccessToken access token 不能用于 refresh → 401。
func TestRefresh_WithAccessToken(t *testing.T) {
	c := newAuthTestClient(t)
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	access, _ := s.GenerateAccessToken(1, "alice", 0)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: access}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestRefresh_InvalidToken 无效 token → 401。
func TestRefresh_InvalidToken(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/refresh", h.refresh)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: "garbage"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

// TestMe_Authenticated 已注入 uid → 200 + 用户信息（无 password_hash）。
func TestMe_Authenticated(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	// 模拟 RequireUser 已注入 uid（通过 X-Vigil-User-ID header，signer=nil 走 header 分支）
	e.GET("/api/v1/auth/me", h.me, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("X-Vigil-User-ID", "1") // alice 的 ID
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, `"username":"alice"`) {
		t.Errorf("response missing alice: %s", body)
	}
	// password_hash 不应泄露
	if contains(body, "password_hash") || contains(body, "PasswordHash") {
		t.Errorf("password_hash leaked in me response: %s", body)
	}
}

// TestMe_Unauthenticated 未注入 uid → 401。
func TestMe_Unauthenticated(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.GET("/api/v1/auth/me", h.me, RequireUser(true, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}

// —— QA 审计 C8：密码强度校验 ——

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		pw   string
		want string // 空串表示通过
	}{
		{"short", "at least 8"}, // 太短
		{"abcdefgh", "two of"},  // 仅字母一类
		{"12345678", "two of"},  // 仅数字一类
		{"abcd1234", ""},        // 字母+数字 → 通过
		{"abcdefg1!", ""},       // 三类 → 通过
		{"Abcd1234", ""},        // 大小写+数字 → 通过
	}
	for _, tc := range cases {
		got := ValidatePasswordStrength(tc.pw)
		if tc.want == "" {
			if got != "" {
				t.Errorf("ValidatePasswordStrength(%q)=%q, want pass", tc.pw, got)
			}
		} else {
			if !contains(got, tc.want) {
				t.Errorf("ValidatePasswordStrength(%q)=%q, want containing %q", tc.pw, got, tc.want)
			}
		}
	}
}

// —— QA 审计 C8：修改密码端点 ——

// TestChangePassword_Success 正确旧密码 + 合格新密码 → 200 + 清除 must_change_password。
func TestChangePassword_Success(t *testing.T) {
	c := newAuthTestClient(t) // alice 密码 "pw"
	h := NewAuthHandler(c, newTestSigner1m())
	// 标记 alice 强制改密
	ctx := context.Background()
	alice, _ := c.User.Query().Where(user.UsernameEQ("alice")).Only(ctx)
	_ = c.User.UpdateOneID(alice.ID).SetMustChangePassword(true).Exec(ctx)

	e := echo.New()
	e.POST("/api/v1/auth/change-password", h.changePassword, RequireUser(true, nil))

	body := changePasswordReq{OldPassword: "pw", NewPassword: "alice-new-123"}
	req := postJSON("/api/v1/auth/change-password", body)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(alice.ID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 验证新密码生效 + 标志清除
	updated, _ := c.User.Get(ctx, alice.ID)
	if !VerifyPassword("alice-new-123", updated.PasswordHash) {
		t.Error("new password not set")
	}
	if updated.MustChangePassword {
		t.Error("must_change_password not cleared after change")
	}
}

// TestChangePassword_RevokesOldTokens 改密后 token_version 自增，旧 access/refresh 立即失效（T0.4）。
// 覆盖核心安全属性：改密前签发的 token 在改密后被 resolver（access）与 refresh 端点判为吊销。
func TestChangePassword_RevokesOldTokens(t *testing.T) {
	c := newAuthTestClient(t) // alice 密码 "pw"，token_version=0
	s := newTestSigner1m()
	h := NewAuthHandler(c, s)
	ctx := context.Background()
	alice, _ := c.User.Query().Where(user.UsernameEQ("alice")).Only(ctx)

	// 改密前用当前版本（0）签发 access + refresh。
	oldAccess, _ := s.GenerateAccessToken(alice.ID, alice.Username, alice.TokenVersion)
	oldRefresh, _ := s.GenerateRefreshToken(alice.ID, alice.TokenVersion)

	// 改密前：resolver 认可旧 access。
	resolver := NewIdentityResolver(s, nil, false, c)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+oldAccess)
	if uid, ok := resolver.Resolve(ctx, hdr); !ok || uid != alice.ID {
		t.Fatalf("改密前旧 access 应有效: uid=%d ok=%v", uid, ok)
	}

	// 执行改密。
	e := echo.New()
	e.POST("/api/v1/auth/change-password", h.changePassword, RequireUser(true, nil))
	req := postJSON("/api/v1/auth/change-password",
		changePasswordReq{OldPassword: "pw", NewPassword: "alice-new-123"})
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(alice.ID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("改密 status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// token_version 应已自增。
	updated, _ := c.User.Get(ctx, alice.ID)
	if updated.TokenVersion != alice.TokenVersion+1 {
		t.Errorf("token_version=%d, want %d", updated.TokenVersion, alice.TokenVersion+1)
	}

	// 改密后：旧 access 被 resolver 判为吊销（版本不匹配）。
	if _, ok := resolver.Resolve(ctx, hdr); ok {
		t.Error("改密后旧 access 仍有效，应被吊销")
	}

	// 改密后：旧 refresh 换 access → 401（版本不匹配）。
	er := echo.New()
	er.POST("/api/v1/auth/refresh", h.refresh)
	rrec := httptest.NewRecorder()
	er.ServeHTTP(rrec, postJSON("/api/v1/auth/refresh", refreshReq{RefreshToken: oldRefresh}))
	if rrec.Code != http.StatusUnauthorized {
		t.Errorf("改密后旧 refresh status %d, want 401; body=%s", rrec.Code, rrec.Body.String())
	}

	// 用新密码重新登录，换发的 token 版本与库一致 → resolver 认可。
	el := echo.New()
	el.POST("/api/v1/auth/login", h.login)
	lrec := httptest.NewRecorder()
	el.ServeHTTP(lrec, postJSON("/api/v1/auth/login", loginReq{Username: "alice", Password: "alice-new-123"}))
	if lrec.Code != http.StatusOK {
		t.Fatalf("改密后重新登录 status %d, want 200; body=%s", lrec.Code, lrec.Body.String())
	}
	var lresp loginResp
	_ = json.Unmarshal(lrec.Body.Bytes(), &lresp)
	nhdr := http.Header{}
	nhdr.Set("Authorization", "Bearer "+lresp.AccessToken)
	if uid, ok := resolver.Resolve(ctx, nhdr); !ok || uid != alice.ID {
		t.Errorf("改密后新 access 应有效: uid=%d ok=%v", uid, ok)
	}
}

// TestResolver_TokenVersionNilDB db 为 nil 时跳过吊销校验（无状态回退，向后兼容）。
func TestResolver_TokenVersionNilDB(t *testing.T) {
	s := newTestSigner1m()
	// 用非 0 版本签发；db=nil 应跳过版本比对直接放行。
	tok, _ := s.GenerateAccessToken(42, "alice", 7)
	resolver := NewIdentityResolver(s, nil, false, nil)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+tok)
	if uid, ok := resolver.Resolve(context.Background(), hdr); !ok || uid != 42 {
		t.Errorf("db=nil 应跳过版本校验: uid=%d ok=%v", uid, ok)
	}
}

// TestChangePassword_WrongOld 旧密码错误 → 401，不改密。
func TestChangePassword_WrongOld(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/change-password", h.changePassword, RequireUser(true, nil))

	req := postJSON("/api/v1/auth/change-password",
		changePasswordReq{OldPassword: "wrong", NewPassword: "alice-new-123"})
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestChangePassword_WeakNew 新密码强度不足 → 400。
func TestChangePassword_WeakNew(t *testing.T) {
	c := newAuthTestClient(t)
	h := NewAuthHandler(c, newTestSigner1m())
	e := echo.New()
	e.POST("/api/v1/auth/change-password", h.changePassword, RequireUser(true, nil))

	req := postJSON("/api/v1/auth/change-password",
		changePasswordReq{OldPassword: "pw", NewPassword: "weak"})
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
