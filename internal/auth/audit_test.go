package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

func TestAuditRecorder_Record(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:audit_rec?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	r := NewAuditRecorder(c)
	ctx := context.Background()

	err := r.Record(ctx, AuditEntry{
		ActorUserID: 1, ActorName: "alice",
		Action: "role.create", ResourceType: "role", ResourceID: 5, ResourceName: "viewer",
		Result: AuditResultSuccess,
		Detail: map[string]any{"permissions": []string{"incident.view"}},
		IP:     "1.2.3.4", UserAgent: "test-agent",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	log, err := c.AuditLog.Query().Only(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if log.ActorUserID != 1 || log.ActorName != "alice" {
		t.Errorf("actor=%+v", log)
	}
	if log.Action != "role.create" || log.ResourceType != "role" {
		t.Errorf("action/resource=%q/%q", log.Action, log.ResourceType)
	}
	if log.Result != auditlog.ResultSuccess {
		t.Errorf("result=%q", log.Result)
	}
	if log.IP != "1.2.3.4" || log.UserAgent != "test-agent" {
		t.Errorf("ip/ua=%q/%q", log.IP, log.UserAgent)
	}
}

func TestAuditRecorder_NilSafe(t *testing.T) {
	// nil recorder 不 panic，静默跳过
	var r *AuditRecorder
	if err := r.Record(context.Background(), AuditEntry{Action: "x"}); err != nil {
		t.Errorf("nil recorder Record returned err: %v", err)
	}
	r.MustRecord(context.Background(), AuditEntry{Action: "x"})
}

func TestAuditRecorder_DefaultResult(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:audit_default?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	r := NewAuditRecorder(c)
	ctx := context.Background()

	// Result 留空，应默认 success
	_ = r.Record(ctx, AuditEntry{Action: "x", ResourceType: "t"})
	log, _ := c.AuditLog.Query().Only(ctx)
	if log.Result != auditlog.ResultSuccess {
		t.Errorf("default result=%q, want success", log.Result)
	}
}

func TestAuditEntryFromRequest_ExtractsIPAndUA(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 8.8.8.8")
	req.Header.Set("User-Agent", "Mozilla/test")
	e := AuditEntryFromRequest(req, 5, "bob")
	if e.IP != "9.9.9.9" {
		t.Errorf("IP=%q, want 9.9.9.9 (first of XFF)", e.IP)
	}
	if e.UserAgent != "Mozilla/test" {
		t.Errorf("UA=%q", e.UserAgent)
	}
	if e.ActorUserID != 5 || e.ActorName != "bob" {
		t.Errorf("actor=%d/%q", e.ActorUserID, e.ActorName)
	}
}

func TestClientIP_RemoteAddrFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "192.168.1.1:54321"
	if got := clientIP(req); got != "192.168.1.1" {
		t.Errorf("clientIP=%q, want 192.168.1.1 (port stripped)", got)
	}
}
