package notification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/escalation"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:notif_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newIncident(t *testing.T, c *ent.Client) *ent.Incident {
	t.Helper()
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("支付服务 5xx 错误率 > 5%").
		SetSeverity("critical").
		SetStatus("triggered").
		SetPriority("p1").
		SetSummary("支付服务告警").
		SetTriggerType("auto").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// TestWebhookChannel_Send 验证 Webhook 通道正确 POST payload 到目标 URL。
func TestWebhookChannel_Send(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	inc := newIncident(t, c)

	ch := NewWebhookChannel(func(*ent.Incident) []string { return []string{srv.URL} })
	msg := &Message{
		Incident: inc,
		Title:    FormatTitle(inc),
		Summary:  FormatSummary(inc, 0),
		Level:    0,
	}
	results, err := ch.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected 1 success result, got %+v", results)
	}
	if received["incident"] != "INC-0001" {
		t.Errorf("received incident: got %v", received["incident"])
	}
	if received["title"] == "" {
		t.Error("title should not be empty in webhook payload")
	}
}

// TestWebhookChannel_Failure 验证非 2xx 响应记为失败。
func TestWebhookChannel_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t)
	inc := newIncident(t, c)
	ch := NewWebhookChannel(func(*ent.Incident) []string { return []string{srv.URL} })
	results, _ := ch.Send(context.Background(), &Message{Incident: inc, Title: "x"})
	if results[0].Success {
		t.Error("expected failure for 500 response")
	}
}

// TestEmailChannel_UnconfiguredSkips 未配置 SMTP 时降级跳过（不发送）。
func TestEmailChannel_UnconfiguredSkips(t *testing.T) {
	ch := &EmailChannel{
		GetEmails: func(targets []Target) []string { return []string{"a@x.com"} },
	}
	if ch.Available() {
		t.Error("unconfigured email channel Available()=true, want false")
	}
	results, _ := ch.Send(context.Background(), &Message{Targets: []Target{{Name: "u1"}}})
	if len(results) != 0 {
		t.Errorf("unconfigured email channel sent %d results, want 0 (degrade)", len(results))
	}
}

// TestEmailChannel_ConfiguredNoGetEmails 配了 SMTP 但无 GetEmails → 不发送。
func TestEmailChannel_ConfiguredNoGetEmails(t *testing.T) {
	ch := &EmailChannel{
		Config: SMTPConfig{Host: "smtp.example.com", Port: 587},
	}
	if !ch.Available() {
		t.Error("configured email channel Available()=false")
	}
	results, _ := ch.Send(context.Background(), &Message{Targets: []Target{}})
	if len(results) != 0 {
		t.Errorf("email without GetEmails sent %d, want 0", len(results))
	}
}

// TestEmailChannel_SMTPAddr 验证 SMTPAddr 拼接（含默认端口）。
func TestEmailChannel_SMTPAddr(t *testing.T) {
	c := SMTPConfig{Host: "smtp.x.com"}
	if got := c.SMTPAddr(); got != "smtp.x.com:25" {
		t.Errorf("default port: %q, want smtp.x.com:25", got)
	}
	c.Port = 587
	if got := c.SMTPAddr(); got != "smtp.x.com:587" {
		t.Errorf("custom port: %q, want smtp.x.com:587", got)
	}
}

// TestNotifier_FulfillEscalationInterface 验证 Notifier 满足 escalation.Notifier 接口。
func TestNotifier_FulfillEscalationInterface(t *testing.T) {
	var _ escalation.Notifier = (*Notifier)(nil)
}

// TestRegistry 验证通道注册表。
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register(&EmailChannel{})
	if _, ok := r.Get("email"); !ok {
		t.Error("email channel not found")
	}
	if _, ok := r.Get("webhook"); ok {
		t.Error("webhook should not be registered")
	}
	if len(r.All()) != 1 {
		t.Errorf("All: got %d channels", len(r.All()))
	}
}

// TestFormatTitle 验证标题格式。
func TestFormatTitle(t *testing.T) {
	c := newTestClient(t)
	inc := newIncident(t, c)
	got := FormatTitle(inc)
	want := "[CRITICAL] INC-0001 支付服务 5xx 错误率 > 5%"
	if got != want {
		t.Errorf("FormatTitle: got %q, want %q", got, want)
	}
}

// TestNotifier_NotifyEscalation 验证分发器把升级事件分发到 webhook。
func TestNotifier_NotifyEscalation(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	inc := newIncident(t, c)

	reg := NewRegistry()
	reg.Register(NewWebhookChannel(func(*ent.Incident) []string { return []string{srv.URL} }))
	n := NewNotifier(reg, []string{"webhook"})

	var recorded []SendResult
	n.SetResultRecorder(func(_ int, r SendResult) { recorded = append(recorded, r) })

	err := n.NotifyEscalation(context.Background(), inc, 0, []escalation.NotifyTarget{
		{UserID: 1, Name: "张三", Source: "schedule"},
	}, nil) // channels=nil：走默认通道（webhook）
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if !called {
		t.Error("webhook was not called")
	}
	if len(recorded) == 0 {
		t.Error("no result recorded")
	}
}
