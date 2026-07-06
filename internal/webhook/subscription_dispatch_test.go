package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kevin/vigil/internal/incident"
)

// stubSubResolver 内存动态订阅解析器（测试桩）。
type stubSubResolver struct {
	subs []Subscription
}

func (s *stubSubResolver) Resolve(_ context.Context) []Subscription { return s.subs }

// TestDispatcher_MergeEnvAndDB env 静态订阅 + DB 动态订阅合并投递：两个端点都收到。
func TestDispatcher_MergeEnvAndDB(t *testing.T) {
	var mu sync.Mutex
	envHits, dbHits := 0, 0
	envSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		envHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	dbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		dbHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer envSrv.Close()
	defer dbSrv.Close()

	d := NewDispatcher([]string{envSrv.URL}) // env 静态订阅
	d.SetSubscriptionResolver(&stubSubResolver{subs: []Subscription{
		{URL: dbSrv.URL}, // DB 动态订阅（无事件过滤=全部）
	}})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if envHits != 1 {
		t.Errorf("env 订阅应收 1 次, got %d", envHits)
	}
	if dbHits != 1 {
		t.Errorf("DB 动态订阅应收 1 次, got %d", dbHits)
	}
}

// TestDispatcher_EventTypeFilter DB 动态订阅按事件类型过滤：不匹配的事件不投递。
func TestDispatcher_EventTypeFilter(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(nil) // 无 env 订阅
	d.SetSubscriptionResolver(&stubSubResolver{subs: []Subscription{
		{URL: srv.URL, EventTypes: []string{"incident.resolved"}}, // 只订阅 resolved
	}})

	// 发 ack 事件：不匹配过滤 → 不投递。
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()
	mu.Lock()
	if hits != 0 {
		t.Errorf("ack 不在订阅事件类型内，不应投递, got %d", hits)
	}
	mu.Unlock()

	// 发 resolved 事件：匹配 → 投递。
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("resolved"))
	d.Close()
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("resolved 在订阅事件类型内，应投递 1 次, got %d", hits)
	}
}

// TestDispatcher_PerSubscriptionSigning DB 动态订阅用各自签名密钥出站；接收端可验签。
func TestDispatcher_PerSubscriptionSigning(t *testing.T) {
	const secret = "sub-secret-xyz"
	var mu sync.Mutex
	var gotSig, gotTS string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		gotSig = r.Header.Get(HeaderSignature)
		gotTS = r.Header.Get(HeaderTimestamp)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(nil)
	d.SetSubscriptionResolver(&stubSubResolver{subs: []Subscription{
		{URL: srv.URL, SigningSecret: secret},
	}})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if gotSig == "" || gotTS == "" {
		t.Fatal("应带签名头（该订阅配了独立密钥）")
	}
	// 用订阅密钥重算签名，应与收到的一致（timestamp + "." + body）。
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(gotTS))
	mac.Write([]byte("."))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("签名不匹配: got %s want %s", gotSig, want)
	}
}

// TestDispatcher_DisabledSubResolverBackCompat 无动态订阅解析器时退化为仅 env（向后兼容）。
func TestDispatcher_DisabledSubResolverBackCompat(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL}) // 只有 env，无 resolver
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("无 resolver 时应仅按 env 投递 1 次, got %d", hits)
	}
}

// TestDispatcher_DedupSameURL 同一 URL 既在 env 又在 DB 命中时只投递一次（去重）。
func TestDispatcher_DedupSameURL(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher([]string{srv.URL})
	d.SetSubscriptionResolver(&stubSubResolver{subs: []Subscription{
		{URL: srv.URL}, // 与 env 同 URL
	}})
	d.OnIncidentChanged(context.Background(), newTestIncident(), incident.Action("ack"))
	d.Close()

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("同 URL 应去重只投递 1 次, got %d", hits)
	}
}

// TestSubscription_Matches 单测事件类型匹配逻辑（空=全部）。
func TestSubscription_Matches(t *testing.T) {
	all := Subscription{}
	if !all.matches("incident.created") || !all.matches("incident.resolved") {
		t.Error("空 EventTypes 应匹配所有事件")
	}
	filtered := Subscription{EventTypes: []string{"incident.created"}}
	if !filtered.matches("incident.created") {
		t.Error("应匹配已订阅事件")
	}
	if filtered.matches("incident.resolved") {
		t.Error("不应匹配未订阅事件")
	}
}
