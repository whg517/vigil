package ticket

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/actionitem"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/webhook"

	"github.com/labstack/echo/v5"

	_ "github.com/mattn/go-sqlite3"
)

// setupCallback 建 team + incident + postmortem + 1 个已建单 ActionItem（external_id=T-9），
// 返回 client / actionItemID / teamID。
func setupCallback(t *testing.T) (*ent.Client, int, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:tkcb_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	tm := c.Team.Create().SetName("ops").SetSlug("ops-" + t.Name()).SaveX(ctx)
	inc := c.Incident.Create().SetNumber("INC-" + t.Name()).SetTitle("t").
		SetSeverity("warning").SetStatus("resolved").SetTeamID(tm.ID).SaveX(ctx)
	pm := c.Postmortem.Create().SetIncidentID(inc.ID).SetStatus("published").
		SetGeneratedBy("human").SetSections(map[string]any{}).SaveX(ctx)
	ai := c.ActionItem.Create().SetDescription("补监控").SetPostmortemID(pm.ID).
		SetTrackerURL("https://tk/T-9").SetExternalID("T-9").SetStatus("open").SaveX(ctx)
	return c, ai.ID, tm.ID
}

// signedCallbackRequest 构造带 HMAC 签名头的回调请求（对拍 handler 验签逻辑）。
func signedCallbackRequest(t *testing.T, path, secret, body string, ts time.Time) *http.Request {
	t.Helper()
	tsStr, sig := webhook.Sign(secret, []byte(body), ts)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(callbackHeaderTimestamp, tsStr)
	req.Header.Set(callbackHeaderSignature, sig)
	return req
}

// callHandler 走 echo 路由跑一次回调（经 Register 挂载，真正解析 :id 路径参数），返回 status + body。
func callHandler(t *testing.T, h *CallbackHandler, _ int, req *http.Request) (int, string) {
	t.Helper()
	e := echo.New()
	h.Register(e.Group(""))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestCallback_UpdatesActionItemStatus 验签通过 + external_id 匹配 → ActionItem 状态更新。
func TestCallback_UpdatesActionItemStatus(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-123" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"closed"}`
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code, resp := callHandler(t, h, integ.ID, req)

	if code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, resp)
	}
	if !strings.Contains(resp, "updated") {
		t.Errorf("want status updated, got %s", resp)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusDone {
		t.Errorf("action item status not updated to done: got %s", got.Status)
	}
}

// TestCallback_MatchesByTrackerURL external_id 未提供时按 tracker_url 兜底匹配。
func TestCallback_MatchesByTrackerURL(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-url" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"tracker_url":"https://tk/T-9","status":"in_progress"}`
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code, resp := callHandler(t, h, integ.ID, req)
	if code != http.StatusOK || !strings.Contains(resp, "updated") {
		t.Fatalf("want 200 updated, got %d: %s", code, resp)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusInProgress {
		t.Errorf("want in_progress, got %s", got.Status)
	}
}

// TestCallback_RejectsBadSignature 签名不匹配 → 401，不改状态。
func TestCallback_RejectsBadSignature(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret("real-secret").SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"closed"}`
	// 用错误密钥签名。
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), "wrong-secret", body, time.Now())
	code, _ := callHandler(t, h, integ.ID, req)
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad signature, got %d", code)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusOpen {
		t.Errorf("status should stay open on rejected callback, got %s", got.Status)
	}
}

// TestCallback_RejectsStaleTimestamp 超窗时间戳 → 401（防重放）。
func TestCallback_RejectsStaleTimestamp(t *testing.T) {
	c, _, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-stale" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"closed"}`
	// 10 分钟前（超 5 分钟窗）。
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now().Add(-10*time.Minute))
	code, _ := callHandler(t, h, integ.ID, req)
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401 for stale timestamp, got %d", code)
	}
}

// TestCallback_NoSecretRejected 集成未配 callback_secret → 401（不给无密钥后门）。
func TestCallback_NoSecretRejected(t *testing.T) {
	c, _, teamID := setupCallback(t)
	ctx := context.Background()
	// 不设 callback_secret。
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"closed"}`
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), "any", body, time.Now())
	code, _ := callHandler(t, h, integ.ID, req)
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401 when no callback secret configured, got %d", code)
	}
}

// TestCallback_UnmatchedIgnored external_id 无对应 ActionItem → 200 ignored（best-effort）。
func TestCallback_UnmatchedIgnored(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-nomatch" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"NOPE-999","status":"closed"}`
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code, resp := callHandler(t, h, integ.ID, req)
	if code != http.StatusOK || !strings.Contains(resp, "ignored") {
		t.Fatalf("want 200 ignored for unmatched, got %d: %s", code, resp)
	}
	// 未匹配不改任何状态。
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusOpen {
		t.Errorf("unmatched callback should not change status, got %s", got.Status)
	}
}

// TestCallback_Idempotent 重复回调同状态 → 第二次 unchanged，不重复变更。
func TestCallback_Idempotent(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-idem" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"done"}`

	req1 := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code1, resp1 := callHandler(t, h, integ.ID, req1)
	if code1 != http.StatusOK || !strings.Contains(resp1, "updated") {
		t.Fatalf("first callback want updated, got %d: %s", code1, resp1)
	}

	req2 := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code2, resp2 := callHandler(t, h, integ.ID, req2)
	if code2 != http.StatusOK || !strings.Contains(resp2, "unchanged") {
		t.Fatalf("second callback want unchanged (idempotent), got %d: %s", code2, resp2)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusDone {
		t.Errorf("status should be done, got %s", got.Status)
	}
}

// TestCallback_UnknownStatus 未知外部状态 → 400（不误改）。
func TestCallback_UnknownStatus(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	secret := "cb-secret-unk" //nolint:gosec // 测试字面量密钥，非真实凭据
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(secret).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	body := `{"external_id":"T-9","status":"quantum-flux"}`
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code, _ := callHandler(t, h, integ.ID, req)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown status, got %d", code)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusOpen {
		t.Errorf("unknown status should not change action item, got %s", got.Status)
	}
}

// TestCallback_EncryptedSecret callback_secret 密文存储时，handler 经 cipher 解密后验签。
func TestCallback_EncryptedSecret(t *testing.T) {
	c, aiID, teamID := setupCallback(t)
	ctx := context.Background()
	cipher := newCbTestCipher(t)
	secret := "encrypted-cb-secret" //nolint:gosec // 测试字面量密钥，非真实凭据
	ciphertext, err := cipher.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint("https://x").SetCallbackSecret(ciphertext).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)
	integ := c.TicketIntegration.Query().FirstX(ctx)

	h := NewCallbackHandler(c, NewEngine(c))
	h.SetCipher(cipher)
	body := `{"external_id":"T-9","status":"closed"}`
	// 用明文密钥签名（外部系统持明文密钥）。
	req := signedCallbackRequest(t, "/webhooks/ticket/"+itoa(integ.ID), secret, body, time.Now())
	code, resp := callHandler(t, h, integ.ID, req)
	if code != http.StatusOK || !strings.Contains(resp, "updated") {
		t.Fatalf("want 200 updated with encrypted secret, got %d: %s", code, resp)
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.Status != actionitem.StatusDone {
		t.Errorf("want done, got %s", got.Status)
	}
}

func newCbTestCipher(t *testing.T) *crypto.Cipher {
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
