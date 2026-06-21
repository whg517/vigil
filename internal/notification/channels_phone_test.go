package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/ent"
)

// newPhoneIncident 辅助构造测试用 incident（无需 db，区别于 channels_test.go 的 newIncident）。
func newPhoneIncident() *ent.Incident {
	return &ent.Incident{ID: 1, Number: "INC-0001", Title: "test", Severity: "critical", Summary: "测试事件"}
}

func TestPhoneChannel_UnconfiguredSkips(t *testing.T) {
	ch := &PhoneChannel{
		GetPhones: func(targets []Target) []string { return []string{"13800000000"} },
	}
	if ch.Available() {
		t.Error("unconfigured phone Available()=true")
	}
	results, _ := ch.Send(context.Background(), &Message{Incident: newPhoneIncident(), Targets: []Target{}})
	if len(results) != 0 {
		t.Errorf("unconfigured phone sent %d, want 0", len(results))
	}
}

func TestPhoneChannel_SendToWebhook(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &PhoneChannel{
		Config:    VoiceProviderConfig{WebhookURL: srv.URL, From: "vigil"},
		GetPhones: func(targets []Target) []string { return []string{"13800000000", "13900000000"} },
	}
	results, err := ch.Send(context.Background(), &Message{
		Incident: newPhoneIncident(), Title: "[CRITICAL] INC-0001 test",
		Summary: "测试", Level: 2,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Errorf("expected 1 success result, got %+v", results)
	}
	// 验证 webhook 收到的 payload
	if received["channel"] != "phone" {
		t.Errorf("payload channel=%v, want phone", received["channel"])
	}
	recipients, _ := received["recipients"].([]any)
	if len(recipients) != 2 {
		t.Errorf("recipients count=%d, want 2", len(recipients))
	}
}

func TestPhoneChannel_WebhookFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := &PhoneChannel{
		Config:    VoiceProviderConfig{WebhookURL: srv.URL},
		GetPhones: func(targets []Target) []string { return []string{"13800000000"} },
	}
	results, _ := ch.Send(context.Background(), &Message{Incident: newPhoneIncident()})
	if len(results) != 1 || results[0].Success {
		t.Errorf("expected 1 failed result, got %+v", results)
	}
	if results[0].Error == "" {
		t.Error("error message empty on failure")
	}
}

func TestSMSChannel_NameAndSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &SMSChannel{
		Config:    VoiceProviderConfig{WebhookURL: srv.URL},
		GetPhones: func(targets []Target) []string { return []string{"13800000000"} },
	}
	if ch.Name() != "sms" {
		t.Errorf("Name=%q, want sms", ch.Name())
	}
	results, _ := ch.Send(context.Background(), &Message{Incident: newPhoneIncident()})
	if len(results) != 1 || !results[0].Success || results[0].Channel != "sms" {
		t.Errorf("sms send result: %+v", results)
	}
}

func TestPhoneChannel_NoRecipients(t *testing.T) {
	ch := &PhoneChannel{
		Config:    VoiceProviderConfig{WebhookURL: "http://example.com/hook"},
		GetPhones: func(targets []Target) []string { return nil },
	}
	results, _ := ch.Send(context.Background(), &Message{Incident: newPhoneIncident()})
	if len(results) != 0 {
		t.Errorf("no recipients sent %d, want 0", len(results))
	}
}

// 确认 PhoneChannel/SMSChannel 实现 Channel 接口。
func TestPhoneChannelsFulfillInterface(t *testing.T) {
	var _ Channel = (*PhoneChannel)(nil)
	var _ Channel = (*SMSChannel)(nil)
	var _ Channel = (*EmailChannel)(nil)
}
