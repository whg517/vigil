package servicesync

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	entservice "github.com/kevin/vigil/ent/service"
)

// mkService 建一个指定来源/供给时间/状态的服务。
func mkService(t *testing.T, c *ent.Client, slug string, src entservice.Source, provisionedAt time.Time, status entservice.Status) *ent.Service {
	t.Helper()
	svc, err := c.Service.Create().
		SetName(slug).SetSlug(slug).
		SetSource(src).SetProvisionedAt(provisionedAt).SetStatus(status).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create service %s: %v", slug, err)
	}
	return svc
}

// mkEventAt 给服务挂一条指定 received_at 的 Event。
func mkEventAt(t *testing.T, c *ent.Client, svcID int, receivedAt time.Time) {
	t.Helper()
	_, err := c.Event.Create().
		SetSourceEventID("e-" + receivedAt.String()).
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusFiring).
		SetSummary("evt").
		SetDedupKey("dk-" + receivedAt.String()).
		SetReceivedAt(receivedAt).
		SetServiceID(svcID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
}

func statusOf(t *testing.T, c *ent.Client, id int) entservice.Status {
	t.Helper()
	s, err := c.Service.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get service %d: %v", id, err)
	}
	return s.Status
}

// TestPrune_DisablesStaleAuto auto 服务供给早于窗口且窗口内无新 Event → 停用。
func TestPrune_DisablesStaleAuto(t *testing.T) {
	c := newClient(t)
	old := time.Now().Add(-40 * 24 * time.Hour)
	svc := mkService(t, c, "stale-svc", entservice.SourceAuto, old, entservice.StatusActive)
	mkEventAt(t, c, svc.ID, old) // 仅有一条 40 天前的旧告警

	n, err := NewPruner(c, 30).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if statusOf(t, c, svc.ID) != entservice.StatusDisabled {
		t.Fatalf("stale auto service should be disabled")
	}
}

// TestPrune_KeepsRecentlyActive auto 服务窗口内有新 Event → 保留。
func TestPrune_KeepsRecentlyActive(t *testing.T) {
	c := newClient(t)
	old := time.Now().Add(-40 * 24 * time.Hour)
	recent := time.Now().Add(-24 * time.Hour)
	svc := mkService(t, c, "active-svc", entservice.SourceAuto, old, entservice.StatusActive)
	mkEventAt(t, c, svc.ID, old)
	mkEventAt(t, c, svc.ID, recent) // 1 天前的新告警 → 不算过期

	n, err := NewPruner(c, 30).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 || statusOf(t, c, svc.ID) != entservice.StatusActive {
		t.Fatalf("service with recent event must stay active (n=%d)", n)
	}
}

// TestPrune_ProtectsNewlyProvisioned 刚被主动同步建出、尚无告警的新服务 → 保留（provisioned_at 保护）。
func TestPrune_ProtectsNewlyProvisioned(t *testing.T) {
	c := newClient(t)
	fresh := time.Now().Add(-24 * time.Hour) // 1 天前供给，还没来过告警
	svc := mkService(t, c, "fresh-svc", entservice.SourceAuto, fresh, entservice.StatusActive)

	n, err := NewPruner(c, 30).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 || statusOf(t, c, svc.ID) != entservice.StatusActive {
		t.Fatalf("newly-provisioned service must be protected (n=%d)", n)
	}
}

// TestPrune_IgnoresManual 手工服务即使长期无告警也绝不触碰。
func TestPrune_IgnoresManual(t *testing.T) {
	c := newClient(t)
	old := time.Now().Add(-40 * 24 * time.Hour)
	svc := mkService(t, c, "manual-svc", entservice.SourceManual, old, entservice.StatusActive)

	n, err := NewPruner(c, 30).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 || statusOf(t, c, svc.ID) != entservice.StatusActive {
		t.Fatalf("manual service must never be pruned (n=%d)", n)
	}
}
