package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v4"
	_ "github.com/mattn/go-sqlite3"
)

// newAPIKeyHandlerTestClient 构造内存库 + 用户（id=1），返回 client。
func newAPIKeyHandlerTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:apikey_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	_, _ = c.User.Create().SetUsername("alice").SetEmail("a@v.local").Save(ctx)
	return c
}

// postJSONWithUser 构造带 X-Vigil-User-ID 的 POST 请求（模拟 RequireUser 已注入 uid）。
func postJSONWithUser(target string, uid int, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	return req
}

// TestAPIKey_Create_Success 创建 → 201 + 返回明文 token（仅一次）。
func TestAPIKey_Create_Success(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	e := echo.New()
	e.POST("/api/v1/api-keys", h.create, RequireUser(true, nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSONWithUser("/api/v1/api-keys", 1, apiKeyCreateReq{Name: "ci-key"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp apiKeyCreateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Plaintext == "" {
		t.Error("plaintext token empty")
	}
	if resp.Prefix == "" {
		t.Error("prefix empty")
	}
	if resp.Name != "ci-key" {
		t.Errorf("name=%q", resp.Name)
	}
	// 响应里不应有 token_hash
	if bytes.Contains(rec.Body.Bytes(), []byte("token_hash")) {
		t.Error("token_hash leaked in response")
	}

	// 库内无明文，只有哈希
	k, _ := c.APIKey.Query().First(context.Background())
	if k.TokenHash == resp.Plaintext {
		t.Error("stored token_hash equals plaintext, want hashed")
	}
}

// TestAPIKey_Create_MissingName 无 name → 400。
func TestAPIKey_Create_MissingName(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	e := echo.New()
	e.POST("/api/v1/api-keys", h.create, RequireUser(true, nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSONWithUser("/api/v1/api-keys", 1, apiKeyCreateReq{}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

// TestAPIKey_Create_WithExpiry 带 expires_in → expires_at 被设置。
func TestAPIKey_Create_WithExpiry(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	e := echo.New()
	e.POST("/api/v1/api-keys", h.create, RequireUser(true, nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSONWithUser("/api/v1/api-keys", 1, apiKeyCreateReq{Name: "temp", ExpiresIn: 24}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	var resp apiKeyCreateResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ExpiresAt == nil {
		t.Error("expires_at nil, want set")
	}
}

// TestAPIKey_List 列出当前用户的 key（不含 token）。
func TestAPIKey_List(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	// 先创建一个
	ctx := context.Background()
	_, hash, _ := GenerateAPIKey()
	_, _ = c.APIKey.Create().SetName("k1").SetTokenHash(hash).SetPrefix("vgl_xxxxxxxx").SetUserID(1).Save(ctx)

	e := echo.New()
	e.GET("/api/v1/api-keys", h.list, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []apiKeyView
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out) != 1 {
		t.Errorf("list len=%d, want 1", len(out))
	}
	if len(out) > 0 && out[0].Prefix != "vgl_xxxxxxxx" {
		t.Errorf("prefix=%q", out[0].Prefix)
	}
	// 列表不含明文 token
	if bytes.Contains(rec.Body.Bytes(), []byte(`"token"`)) {
		t.Error("list response contains token field")
	}
}

// TestAPIKey_Delete_Success 删除自己的 key。
func TestAPIKey_Delete_Success(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	ctx := context.Background()
	_, hash, _ := GenerateAPIKey()
	k, _ := c.APIKey.Create().SetName("k1").SetTokenHash(hash).SetPrefix("vgl_xxxxxxxx").SetUserID(1).Save(ctx)

	e := echo.New()
	e.DELETE("/api/v1/api-keys/:id", h.delete, RequireUser(true, nil))
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/api-keys/"+strconv.Itoa(k.ID), nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status %d, want 204", rec.Code)
	}
	// 确认已删
	cnt, _ := c.APIKey.Query().Count(ctx)
	if cnt != 0 {
		t.Errorf("after delete count=%d, want 0", cnt)
	}
}

// TestAPIKey_Delete_OthersKey 删别人的 key → 403（防越权）。
func TestAPIKey_Delete_OthersKey(t *testing.T) {
	c := newAPIKeyHandlerTestClient(t)
	h := NewAPIKeyHandler(c)
	ctx := context.Background()
	// 用户 2 的 key
	_, _ = c.User.Create().SetUsername("bob").SetEmail("b@v.local").Save(ctx)
	_, hash, _ := GenerateAPIKey()
	k, _ := c.APIKey.Create().SetName("bobs").SetTokenHash(hash).SetPrefix("vgl_bobxxxxxx").SetUserID(2).Save(ctx)

	e := echo.New()
	e.DELETE("/api/v1/api-keys/:id", h.delete, RequireUser(true, nil))
	// 用户 1 试图删用户 2 的 key
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/api-keys/"+strconv.Itoa(k.ID), nil)
	req.Header.Set("X-Vigil-User-ID", "1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	// key 仍在
	cnt, _ := c.APIKey.Query().Count(ctx)
	if cnt != 1 {
		t.Errorf("key deleted despite 403, count=%d", cnt)
	}
}
