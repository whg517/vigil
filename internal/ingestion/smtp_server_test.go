package ingestion

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/rawevent"

	_ "github.com/mattn/go-sqlite3"
)

// newSMTPTestEnv 内存库 + email 接入点 + 随机端口 SMTP 服务(handler 无 queue,落库即止)。
func newSMTPTestEnv(t *testing.T) (*ent.Client, *ent.Integration, string) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", fmt.Sprintf("file:smtp_%s?mode=memory&cache=shared&_fk=1", t.Name()))
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team := c.Team.Create().SetName("t").SetSlug("t-" + strings.ToLower(t.Name())).SaveX(ctx)
	svc := c.Service.Create().SetName("s").SetSlug("s-" + strings.ToLower(t.Name())).SetTeam(team).SaveX(ctx)
	integ := c.Integration.Create().SetName("mail-in").SetType("email").
		SetToken("tok-" + strings.ToLower(t.Name())).SetService(svc).SetEnabled(true).SaveX(ctx)

	// 随机可用端口
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	srv := NewSMTPServer(c, NewHandler(c, nil), addr)
	srv.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	// 等监听就绪
	for range 50 {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c, integ, addr
}

// TestSMTPInbound_EndToEnd 真实 SMTP 投递 → RawEvent 落库(信封含主题/正文/Message-ID)。
func TestSMTPInbound_EndToEnd(t *testing.T) {
	c, integ, addr := newSMTPTestEnv(t)

	msg := strings.Join([]string{
		"From: zabbix@legacy.local",
		"To: " + integ.Token + "@vigil.local",
		"Subject: [CRITICAL] disk full on db-1",
		"Message-ID: <mid-123@legacy.local>",
		"",
		"Free disk space is less than 5% on volume /",
	}, "\r\n")
	if err := smtp.SendMail(addr, nil, "zabbix@legacy.local",
		[]string{integ.Token + "@vigil.local"}, []byte(msg)); err != nil {
		t.Fatalf("send mail: %v", err)
	}

	raws := c.RawEvent.Query().Where(rawevent.HasIntegrationWith()).AllX(context.Background())
	if len(raws) != 1 {
		t.Fatalf("want 1 raw event, got %d", len(raws))
	}
	payload := string(raws[0].Payload)
	for _, want := range []string{"[CRITICAL] disk full on db-1", "mid-123@legacy.local", "Free disk space"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload missing %q: %s", want, payload)
		}
	}
	if raws[0].Status != rawevent.StatusReceived {
		t.Errorf("status = %s, want received", raws[0].Status)
	}
}

// TestSMTPInbound_UnknownRecipientRejected 未知/禁用 token 在 RCPT 阶段拒收,不落库。
func TestSMTPInbound_UnknownRecipientRejected(t *testing.T) {
	c, _, addr := newSMTPTestEnv(t)

	err := smtp.SendMail(addr, nil, "x@y", []string{"no-such-token@vigil.local"},
		[]byte("Subject: x\r\n\r\nbody"))
	if err == nil {
		t.Fatal("unknown recipient should be rejected")
	}
	if !strings.Contains(err.Error(), "550") {
		t.Errorf("want 550 rejection, got: %v", err)
	}
	if n := c.RawEvent.Query().CountX(context.Background()); n != 0 {
		t.Errorf("rejected mail must not persist, got %d raw events", n)
	}
}

// TestEmailAdapter_Normalize 信封归一化:severity/status/去重键/severity_map 覆盖。
func TestEmailAdapter_Normalize(t *testing.T) {
	a := EmailAdapter{}

	// [CRITICAL] 前缀
	env := []byte(`{"from":"z@l","subject":"[CRITICAL] disk full","message_id":"m1","body":"line1\nline2"}`)
	evts, err := a.Normalize(context.Background(), env, nil, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	e := evts[0]
	if e.Severity != "critical" || e.Status != "firing" || e.SourceEventID != "m1" {
		t.Errorf("got sev=%s status=%s src=%s", e.Severity, e.Status, e.SourceEventID)
	}

	// [RESOLVED] 前缀 → resolved,同 Message-ID 才能关联 firing
	env = []byte(`{"from":"z@l","subject":"[RESOLVED] disk full","message_id":"m1","body":""}`)
	evts, _ = a.Normalize(context.Background(), env, nil, nil)
	if evts[0].Status != "resolved" {
		t.Errorf("resolved prefix: got %s", evts[0].Status)
	}

	// 中文关键词
	env = []byte(`{"from":"z@l","subject":"数据库紧急故障","message_id":"m2","body":""}`)
	evts, _ = a.Normalize(context.Background(), env, nil, nil)
	if evts[0].Severity != "critical" {
		t.Errorf("中文紧急: got %s", evts[0].Severity)
	}

	// severity_map 覆盖:[disaster] 前缀经覆盖表 → critical
	integ := &ent.Integration{Config: map[string]any{"severity_map": map[string]any{"disaster": "critical"}}}
	env = []byte(`{"from":"z@l","subject":"[disaster] core down","message_id":"m3","body":""}`)
	evts, _ = a.Normalize(context.Background(), env, integ, nil)
	if evts[0].Severity != "critical" {
		t.Errorf("severity_map disaster: got %s", evts[0].Severity)
	}

	// 无 Message-ID:指纹去重键稳定
	env = []byte(`{"from":"z@l","subject":"warn thing","date":"d1","body":""}`)
	e1, _ := a.Normalize(context.Background(), env, nil, nil)
	e2, _ := a.Normalize(context.Background(), env, nil, nil)
	if e1[0].SourceEventID == "" || e1[0].SourceEventID != e2[0].SourceEventID {
		t.Errorf("fingerprint unstable: %s vs %s", e1[0].SourceEventID, e2[0].SourceEventID)
	}
}
