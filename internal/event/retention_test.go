// retention_test.go T6.2 Event/RawEvent 保留清理测试：
//   - 按保留期清理旧 Event（超期 + 无关联/已 closed 才删）
//   - ★ 保护活跃 Incident 的 Event（关联未 closed 的 Incident 即使超期也不删）
//   - RawEvent 按 created_at 清理，且不删 requeued（待回灌）
//   - 保留期<=0 不清理（向后兼容）
//   - 分页批量删（batch 小于总量时多批删净）
package event

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	entevent "github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/rawevent"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:evt_retention_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkEvent 建一个指定 created_at 的 Event，可选关联 Incident。
func mkEvent(t *testing.T, c *ent.Client, srcID string, createdAt time.Time, inc *ent.Incident) *ent.Event {
	t.Helper()
	b := c.Event.Create().
		SetSourceEventID(srcID).
		SetSource("prometheus").
		SetSeverity(entevent.SeverityCritical).
		SetStatus(entevent.StatusFiring).
		SetSummary("s-" + srcID).
		SetDedupKey("dk-" + srcID).
		SetCreatedAt(createdAt).
		SetReceivedAt(createdAt)
	if inc != nil {
		b.SetIncident(inc)
	}
	e, err := b.Save(context.Background())
	if err != nil {
		t.Fatalf("create event %s: %v", srcID, err)
	}
	return e
}

// mkIncident 建一个指定 status 的 Incident。
func mkIncident(t *testing.T, c *ent.Client, number string, status incident.Status) *ent.Incident {
	t.Helper()
	inc, err := c.Incident.Create().
		SetNumber(number).
		SetTitle("t-" + number).
		SetSeverity(incident.SeverityCritical).
		SetStatus(status).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create incident %s: %v", number, err)
	}
	return inc
}

func TestSweep_DeletesOldUnlinkedEvents(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour)  // 100 天前
	fresh := time.Now().Add(-10 * 24 * time.Hour) // 10 天前

	mkEvent(t, c, "old-unlinked", old, nil)     // 应删：超期 + 无关联
	mkEvent(t, c, "fresh-unlinked", fresh, nil) // 应留：未超期

	// 保留期 90 天。
	s := NewRetentionSweeper(c, 90, 0, 0, 0)
	ev, _, err := s.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ev != 1 {
		t.Fatalf("expected 1 event deleted, got %d", ev)
	}
	// fresh 仍在，old 已删。
	remaining, _ := c.Event.Query().All(ctx)
	if len(remaining) != 1 || remaining[0].SourceEventID != "fresh-unlinked" {
		t.Fatalf("expected only fresh-unlinked remaining, got %+v", remaining)
	}
}

func TestSweep_ProtectsActiveIncidentEvents(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	old := time.Now().Add(-200 * 24 * time.Hour) // 远超保留期

	activeInc := mkIncident(t, c, "INC-1", incident.StatusAcked)  // 活跃（未 closed）
	closedInc := mkIncident(t, c, "INC-2", incident.StatusClosed) // 已 closed

	mkEvent(t, c, "linked-active", old, activeInc) // ★ 应留：关联活跃 Incident，即使超期
	mkEvent(t, c, "linked-closed", old, closedInc) // 应删：关联的 Incident 已 closed
	mkEvent(t, c, "unlinked", old, nil)            // 应删：无关联

	s := NewRetentionSweeper(c, 30, 0, 0, 0)
	ev, _, err := s.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ev != 2 {
		t.Fatalf("expected 2 events deleted (closed + unlinked), got %d", ev)
	}
	// 唯一剩下的必须是关联活跃 Incident 的那条。
	remaining, _ := c.Event.Query().All(ctx)
	if len(remaining) != 1 || remaining[0].SourceEventID != "linked-active" {
		t.Fatalf("active-incident event must survive; remaining=%+v", remaining)
	}
}

func TestSweep_RawEventsByAgeExcludeRequeued(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	old := time.Now().Add(-60 * 24 * time.Hour)
	fresh := time.Now().Add(-5 * 24 * time.Hour)

	mkRaw := func(status rawevent.Status, createdAt time.Time) {
		if _, err := c.RawEvent.Create().
			SetPayload([]byte("{}")).
			SetStatus(status).
			SetCreatedAt(createdAt).
			SetReceivedAt(createdAt).
			Save(ctx); err != nil {
			t.Fatalf("create raw event: %v", err)
		}
	}
	mkRaw(rawevent.StatusNormalized, old)   // 应删：超期终态
	mkRaw(rawevent.StatusParseFailed, old)  // 应删：超期终态
	mkRaw(rawevent.StatusRequeued, old)     // ★ 应留：requeued 待回灌，删了丢告警
	mkRaw(rawevent.StatusNormalized, fresh) // 应留：未超期

	// raw 保留期 30 天，event 清理关闭。
	s := NewRetentionSweeper(c, 0, 30, 0, 0)
	_, raw, err := s.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if raw != 2 {
		t.Fatalf("expected 2 raw events deleted, got %d", raw)
	}
	// requeued 与 fresh 仍在。
	requeued, _ := c.RawEvent.Query().Where(rawevent.StatusEQ(rawevent.StatusRequeued)).Count(ctx)
	if requeued != 1 {
		t.Fatalf("requeued raw event must survive, got count=%d", requeued)
	}
	total, _ := c.RawEvent.Query().Count(ctx)
	if total != 2 {
		t.Fatalf("expected 2 raw events remaining, got %d", total)
	}
}

func TestSweep_DisabledWhenZeroRetention(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	old := time.Now().Add(-500 * 24 * time.Hour)
	mkEvent(t, c, "ancient", old, nil)

	s := NewRetentionSweeper(c, 0, 0, 0, 0) // 全关闭
	if s.Enabled() {
		t.Fatal("sweeper should be disabled when both retention periods are 0")
	}
	ev, raw, err := s.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ev != 0 || raw != 0 {
		t.Fatalf("disabled sweeper must delete nothing, got ev=%d raw=%d", ev, raw)
	}
	if cnt, _ := c.Event.Query().Count(ctx); cnt != 1 {
		t.Fatalf("ancient event must survive when retention disabled, count=%d", cnt)
	}
}

func TestSweep_BatchPaginatesUntilDrained(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour)
	// 建 7 条超期无关联 Event，batch=2 → 需多批才能删净。
	for i := 0; i < 7; i++ {
		mkEvent(t, c, "old-"+strconv.Itoa(i), old.Add(time.Duration(i)*time.Minute), nil)
	}
	s := NewRetentionSweeper(c, 90, 0, 2, 0) // batch=2
	ev, _, err := s.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ev != 7 {
		t.Fatalf("expected all 7 events deleted across batches, got %d", ev)
	}
	if cnt, _ := c.Event.Query().Count(ctx); cnt != 0 {
		t.Fatalf("expected 0 events remaining, got %d", cnt)
	}
}
