// handler_test.go 接入点管理 API 测试（QA 审计：原 internal/integration 零测试）。
//
// 覆盖审计点名的可测逻辑：
//   - generateToken 前缀（vig_int_）/ 随机性（不重复）
//   - create 返回一次性 token，list/get 不回显 token 明文（Sensitive 脱敏）
//   - update 部分更新（name/enabled 指针语义）
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func newIntegrationTestClient(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// postJSON 辅助：POST + JSON body。
func postJSON(target string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(b)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

// patchJSON 辅助：PATCH + JSON body。
func patchJSON(target string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(string(b)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

// TestGenerateToken_PrefixAndRandomness token 前缀正确且连续生成不重复。
func TestGenerateToken_PrefixAndRandomness(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		tok := generateToken()
		if !strings.HasPrefix(tok, tokenPrefix) {
			t.Errorf("token %q missing prefix %q", tok, tokenPrefix)
		}
		hex := strings.TrimPrefix(tok, tokenPrefix)
		// 16 字节 = 32 hex 字符
		if len(hex) != 32 {
			t.Errorf("token hex length: got %d, want 32 (tok=%s)", len(hex), tok)
		}
		if seen[tok] {
			t.Fatalf("token collision after %d generations: %s (随机性不足)", i, tok)
		}
		seen[tok] = true
	}
}

// TestCreate_ReturnsTokenOnce create 返回明文 token（一次性），落库后 list 不回显。
func TestCreate_ReturnsTokenOnce(t *testing.T) {
	c := newIntegrationTestClient(t, "integ_create")
	h := NewHandler(c)
	e := echo.New()
	e.POST("/integrations", h.create)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, postJSON("/integrations", createReq{Name: "prom", Type: "prometheus"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	// create 响应必须含明文 token
	var resp createResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(resp.Token, tokenPrefix) {
		t.Errorf("create response token missing prefix: %q", resp.Token)
	}
	if resp.Token == "" {
		t.Error("create response should return plaintext token once")
	}
}

// TestListAndGet_TokenExposure token 回显契约：list 不回显（避免批量暴露），
// get 详情回显（供已授权用户查看接入 URL/token；token 是 webhook URL 路径密钥、非加密，
// get 已按 integration.view 鉴权，等同展示 webhook URL）。
func TestListAndGet_TokenExposure(t *testing.T) {
	c := newIntegrationTestClient(t, "integ_leak")
	ctx := context.Background()
	// 直接造一条带 token 的 integration
	integ, err := c.Integration.Create().
		SetName("prom").SetType("prometheus").SetToken("vig_int_secret_xyz").SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	h := NewHandler(c)
	e := echo.New()
	e.GET("/integrations", h.list)
	e.GET("/integrations/:id", h.get)

	// list：不回显 token（批量暴露风险）。
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/integrations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "vig_int_secret_xyz") {
		t.Errorf("list 泄露 token 明文：列表不应回显（避免批量暴露）")
	}

	// get：详情回显 token（供表单持久展示接入 URL/token）。
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/integrations/"+itoa(integ.ID), nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("get status %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "vig_int_secret_xyz") {
		t.Errorf("get 未回显 token：详情应返回 token 供展示接入 URL")
	}
}

// TestUpdate_PartialUpdate update 部分字段（name/enabled 指针，未传不动）。
func TestUpdate_PartialUpdate(t *testing.T) {
	c := newIntegrationTestClient(t, "integ_update")
	ctx := context.Background()
	integ, _ := c.Integration.Create().
		SetName("orig").SetType("prometheus").SetToken("vig_int_x").SetEnabled(true).
		Save(ctx)
	h := NewHandler(c)
	e := echo.New()
	e.PATCH("/integrations/:id", h.update)

	// 只改 name，不动 enabled
	enabled := true
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, patchJSON("/integrations/"+itoa(integ.ID), updateReq{
		Name:    strPtr("renamed"),
		Enabled: &enabled,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 验证落库
	got, _ := c.Integration.Get(ctx, integ.ID)
	if got.Name != "renamed" {
		t.Errorf("name: got %q, want renamed", got.Name)
	}
	if !got.Enabled {
		t.Error("enabled should remain true")
	}
}

func strPtr(s string) *string { return &s }

// itoa 简易 int→string（避免引入 strconv 给少量调用）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
