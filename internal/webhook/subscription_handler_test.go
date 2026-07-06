// subscription_handler_test.go 出站 webhook 动态订阅 CRUD + 解析器测试（N2.2）。
package webhook

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
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/incident"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func subTestClient(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// subHandlerRoutes 挂载 SubscriptionHandler 全部路由到一个 echo 实例（无 authz——单测走降级放行）。
func subHandlerRoutes(h *SubscriptionHandler) *echo.Echo {
	e := echo.New()
	e.GET("/webhook-subscriptions", h.list)
	e.POST("/webhook-subscriptions", h.create)
	e.GET("/webhook-subscriptions/:id", h.get)
	e.PATCH("/webhook-subscriptions/:id", h.update)
	e.DELETE("/webhook-subscriptions/:id", h.delete)
	return e
}

// TestSubscriptionCRUD 创建 → 详情（密钥不回显）→ 列表 → 删除。
func TestSubscriptionCRUD(t *testing.T) {
	c := subTestClient(t, "sub_crud")
	cipher, _ := crypto.NewCipher("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") // 32 字节 key
	h := NewSubscriptionHandler(c)
	h.SetCipher(cipher)
	e := subHandlerRoutes(h)

	// 创建（带独立签名密钥 + 事件类型过滤）。
	body := `{"name":"prod","url":"http://receiver.example/hook","event_types":["incident.created"],"signing_secret":"topsecret"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook-subscriptions", bytes.NewReader([]byte(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID            int    `json:"id"`
		URL           string `json:"url"`
		SigningSecret string `json:"signing_secret"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == 0 {
		t.Fatal("created id should be non-zero")
	}
	// ★ signing_secret 绝不回显（Sensitive）。
	if created.SigningSecret != "" {
		t.Errorf("signing_secret must not be returned, got %q", created.SigningSecret)
	}

	// 密文落库（非明文）。
	stored, _ := c.WebhookSubscription.Get(context.Background(), created.ID)
	if stored.SigningSecret == "topsecret" {
		t.Error("signing_secret should be stored encrypted, not plaintext")
	}
	if stored.SigningSecret == "" {
		t.Error("signing_secret should be stored (encrypted)")
	}

	// 详情：密钥不回显。
	req = httptest.NewRequest(http.MethodGet, "/webhook-subscriptions/"+strconv.Itoa(created.ID), nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("topsecret")) {
		t.Error("get must not expose signing_secret")
	}

	// 列表。
	req = httptest.NewRequest(http.MethodGet, "/webhook-subscriptions", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d", rec.Code)
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(list))
	}

	// 删除。
	req = httptest.NewRequest(http.MethodDelete, "/webhook-subscriptions/"+strconv.Itoa(created.ID), nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if n, _ := c.WebhookSubscription.Query().Count(context.Background()); n != 0 {
		t.Errorf("subscription should be deleted, count=%d", n)
	}
}

// TestSubscriptionCreate_MissingURL 缺 url → 400。
func TestSubscriptionCreate_MissingURL(t *testing.T) {
	c := subTestClient(t, "sub_missing_url")
	h := NewSubscriptionHandler(c)
	e := subHandlerRoutes(h)
	req := httptest.NewRequest(http.MethodPost, "/webhook-subscriptions", bytes.NewReader([]byte(`{"name":"x"}`)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// TestSubscriptionUpdate_Disable 更新 enabled=false → 停用（dispatcher 不再解析到）。
func TestSubscriptionUpdate_Disable(t *testing.T) {
	c := subTestClient(t, "sub_disable")
	h := NewSubscriptionHandler(c)
	e := subHandlerRoutes(h)
	sub, _ := c.WebhookSubscription.Create().SetURL("http://x").SetEnabled(true).Save(context.Background())

	req := httptest.NewRequest(http.MethodPatch, "/webhook-subscriptions/"+strconv.Itoa(sub.ID),
		bytes.NewReader([]byte(`{"enabled":false}`)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 解析器只返 enabled=true 的订阅：停用后应查不到。
	r := NewEntSubscriptionResolver(c, nil)
	if got := r.Resolve(context.Background()); len(got) != 0 {
		t.Errorf("disabled subscription should not be resolved, got %d", len(got))
	}
}

// TestEntSubscriptionResolver_DecryptSecret 解析器读出订阅并解密签名密钥。
func TestEntSubscriptionResolver_DecryptSecret(t *testing.T) {
	c := subTestClient(t, "sub_resolver")
	cipher, _ := crypto.NewCipher("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	enc, _ := cipher.Encrypt("plainsecret")
	_, _ = c.WebhookSubscription.Create().
		SetURL("http://x").SetEnabled(true).
		SetEventTypes([]string{"incident.created"}).
		SetSigningSecret(enc).
		Save(context.Background())

	r := NewEntSubscriptionResolver(c, cipher)
	subs := r.Resolve(context.Background())
	if len(subs) != 1 {
		t.Fatalf("expected 1 resolved sub, got %d", len(subs))
	}
	if subs[0].URL != "http://x" {
		t.Errorf("url = %q", subs[0].URL)
	}
	if len(subs[0].EventTypes) != 1 || subs[0].EventTypes[0] != "incident.created" {
		t.Errorf("event_types = %+v", subs[0].EventTypes)
	}
	// 解密后应是明文（供出站签名）。
	if subs[0].SigningSecret != "plainsecret" {
		t.Errorf("signing_secret should be decrypted to plaintext, got %q", subs[0].SigningSecret)
	}
}

// TestSubscriptionEndToEnd_DBDrivenDelivery 建订阅 → dispatcher 经解析器投递到该 URL（含事件过滤）。
func TestSubscriptionEndToEnd_DBDrivenDelivery(t *testing.T) {
	c := subTestClient(t, "sub_e2e")
	// 建一条只订阅 incident.resolved 的动态订阅。
	_, _ = c.WebhookSubscription.Create().
		SetURL("http://placeholder").SetEnabled(true).
		SetEventTypes([]string{"incident.resolved"}).
		Save(context.Background())
	// 用真实 receiver 覆盖 URL（建后改）。
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sub, _ := c.WebhookSubscription.Query().Only(context.Background())
	_, _ = c.WebhookSubscription.UpdateOneID(sub.ID).SetURL(srv.URL).Save(context.Background())

	d := NewDispatcher(nil)
	d.SetSubscriptionResolver(NewEntSubscriptionResolver(c, nil))

	// 发不匹配事件（ack）→ 不投递。
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()
	if hits != 0 {
		t.Errorf("ack not in subscription filter, should not deliver, got %d", hits)
	}
	// 发匹配事件（resolved）→ 投递。
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("resolved"))
	d.Close()
	if hits != 1 {
		t.Errorf("resolved matches filter, should deliver once, got %d", hits)
	}
}
