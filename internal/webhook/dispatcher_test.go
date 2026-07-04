package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// stubRecorder 内存投递记录器（测试桩）。
type stubRecorder struct {
	mu   sync.Mutex
	recs []DeliveryRecord
}

func (s *stubRecorder) RecordDelivery(_ context.Context, r DeliveryRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
}

func (s *stubRecorder) all() []DeliveryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeliveryRecord, len(s.recs))
	copy(out, s.recs)
	return out
}

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

// TestDispatcher_Signature 验证 S13：配置密钥后出站带签名头，且签名可被同一算法验源（含时间戳防重放基串）。
func TestDispatcher_Signature(t *testing.T) {
	const secret = "s3cr3t"
	var mu sync.Mutex
	var gotSig, gotTs string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig = r.Header.Get(HeaderSignature)
		gotTs = r.Header.Get(HeaderTimestamp)
		gotBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL})
	d.SetSigningSecret(secret)
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if gotSig == "" || gotTs == "" {
		t.Fatalf("应带签名头: sig=%q ts=%q", gotSig, gotTs)
	}
	// 接收端用同一密钥 + 收到的 timestamp + body 重算，须与收到的签名一致（验源）。
	if !verifySig(secret, gotTs, gotBody, gotSig) {
		t.Errorf("签名重算不匹配：sig=%s ts=%s", gotSig, gotTs)
	}
	// 错误密钥不应通过（防伪造）。
	if verifySig("wrong", gotTs, gotBody, gotSig) {
		t.Error("错误密钥不应验签通过")
	}
}

// verifySig 用与 dispatcher.sign 相同的算法（timestamp + "." + body）重算并比对。
func verifySig(secret, ts string, body []byte, sig string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)) == sig
}

// TestDispatcher_NoSignatureWhenSecretEmpty 验证未配密钥时不签名（向后兼容）。
func TestDispatcher_NoSignatureWhenSecretEmpty(t *testing.T) {
	var mu sync.Mutex
	var gotSig, gotTs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotSig = r.Header.Get(HeaderSignature)
		gotTs = r.Header.Get(HeaderTimestamp)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL}) // 不设密钥
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if gotSig != "" || gotTs != "" {
		t.Errorf("未配密钥不应签名: sig=%q ts=%q", gotSig, gotTs)
	}
}

// TestDispatcher_DeadLetterOnExhaustedFailure 验证 C24：重试耗尽仍失败落死信记录（失败 payload/错误可查）。
func TestDispatcher_DeadLetterOnExhaustedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 恒失败
	}))
	defer srv.Close()

	rec := &stubRecorder{}
	d := NewDispatcher([]string{srv.URL})
	d.SetDeliveryRecorder(rec)
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("resolve"))
	d.Close()

	recs := rec.all()
	if len(recs) != 1 {
		t.Fatalf("应落 1 条投递记录，实际 %d", len(recs))
	}
	r := recs[0]
	if r.Success {
		t.Error("恒失败应记 Success=false（死信）")
	}
	if r.LastStatusCode != http.StatusInternalServerError {
		t.Errorf("last status: got %d", r.LastStatusCode)
	}
	if r.LastError == "" {
		t.Error("死信应含错误原因")
	}
	if r.Event != "incident.resolve" {
		t.Errorf("event: got %s", r.Event)
	}
	if len(r.Payload) == 0 {
		t.Error("死信应留存 payload 供重放")
	}
}

// TestDispatcher_RecordSuccess 验证成功也落记录（送达率可观测）。
func TestDispatcher_RecordSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &stubRecorder{}
	d := NewDispatcher([]string{srv.URL})
	d.SetDeliveryRecorder(rec)
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	recs := rec.all()
	if len(recs) != 1 || !recs[0].Success {
		t.Fatalf("成功应落 1 条 Success=true，实际 %+v", recs)
	}
}

// TestDispatcher_SendOnce 验证同步单发（重放复用）：成功/失败均正确反馈。
func TestDispatcher_SendOnce(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failSrv.Close()

	d := NewDispatcher(nil) // SendOnce 不依赖订阅列表
	if res := d.SendOnce(context.Background(), okSrv.URL, []byte(`{}`)); !res.Success || res.StatusCode != 200 {
		t.Errorf("okSrv: got %+v", res)
	}
	if res := d.SendOnce(context.Background(), failSrv.URL, []byte(`{}`)); res.Success || res.StatusCode != http.StatusBadGateway {
		t.Errorf("failSrv: got %+v", res)
	}
}
