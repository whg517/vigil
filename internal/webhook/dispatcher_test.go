package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kevin/vigil/ent"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/incident"
)

// newTestIncident 构造测试用 incident（不入库）。
func newTestIncident() *ent.Incident {
	return &ent.Incident{
		ID:       42,
		Number:   "INC-0042",
		Title:    "支付5xx",
		Severity: "critical",
		Status:   "acked",
		Summary:  "支付服务5xx",
	}
}

// TestDispatcher_NoSubscriptions 验证无订阅时不推送。
func TestDispatcher_NoSubscriptions(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	d := NewDispatcher(nil) // 无订阅
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	if called {
		t.Error("无订阅时不应推送")
	}
}

// TestDispatcher_PushSuccess 验证推送成功。
func TestDispatcher_PushSuccess(t *testing.T) {
	var received map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		mu.Lock()
		received = m
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close() // 等待异步推送完成

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("未收到推送")
	}
	if received["event"] != "incident.ack" {
		t.Errorf("event: got %v", received["event"])
	}
	if received["incident"] != "INC-0042" {
		t.Errorf("incident: got %v", received["incident"])
	}
	if received["status"] != "acked" {
		t.Errorf("status: got %v", received["status"])
	}
}

// TestDispatcher_RetryOnFailure 验证失败重试（最终成功）。
func TestDispatcher_RetryOnFailure(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		cc := callCount
		mu.Unlock()
		if cc < 3 {
			w.WriteHeader(http.StatusInternalServerError) // 前两次失败
			return
		}
		w.WriteHeader(http.StatusOK) // 第三次成功
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("resolve"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if callCount != 3 {
		t.Errorf("应重试到第 3 次成功，实际调用 %d 次", callCount)
	}
}

// TestDispatcher_MultipleURLs 验证推送给多个订阅者。
func TestDispatcher_MultipleURLs(t *testing.T) {
	var mu sync.Mutex
	count1, count2 := 0, 0
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count1++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count2++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()
	defer srv2.Close()

	d := NewDispatcher([]string{srv1.URL, srv2.URL})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if count1 != 1 || count2 != 1 {
		t.Errorf("每个订阅者应各收 1 次: count1=%d count2=%d", count1, count2)
	}
}

// TestDispatcher_HasSubscriptions 验证订阅判断。
func TestDispatcher_HasSubscriptions(t *testing.T) {
	if NewDispatcher(nil).HasSubscriptions() {
		t.Error("无 URL 时 HasSubscriptions 应为 false")
	}
	if !NewDispatcher([]string{"http://x"}).HasSubscriptions() {
		t.Error("有 URL 时 HasSubscriptions 应为 true")
	}
}

// TestDispatcher_CreatedAndEscalatedEvents 验证 C24：出站 webhook 覆盖 created 与升级事件。
// 经领域事件总线（OnIncidentEvent）分发时，created/escalate 都能出站，且 event 名正确。
func TestDispatcher_CreatedAndEscalatedEvents(t *testing.T) {
	cases := []struct {
		action    string
		wantEvent string
	}{
		{"created", "incident.created"},   // B10/C24：新告警建单出站
		{"escalate", "incident.escalate"}, // B10：自动升级出站
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			var mu sync.Mutex
			var received map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				mu.Lock()
				received = m
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			d := NewDispatcher([]string{srv.URL})
			_ = d.OnIncidentEvent(context.Background(), domainevent.Event{
				Type:     domainevent.IncidentCreated,
				Incident: newTestIncident(),
				Action:   domainevent.Action(tc.action),
			})
			d.Close()

			mu.Lock()
			defer mu.Unlock()
			if received == nil {
				t.Fatalf("%s 未收到出站推送", tc.action)
			}
			if received["event"] != tc.wantEvent {
				t.Errorf("event: got %v, want %v", received["event"], tc.wantEvent)
			}
		})
	}
}
