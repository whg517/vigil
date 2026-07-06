// sources_test.go 失败率来源（entFailureRate）测试。
//
// 核心断言防递归边界：自监控告警走 unrouted（无 Incident 关联），其送达记录不得计入
// 业务失败率——否则「自告警失败 → 失败率升高 → 再触发 → 循环」。
package selfmon

import (
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	entincident "github.com/kevin/vigil/ent/incident"
	entnotification "github.com/kevin/vigil/ent/notification"

	_ "github.com/mattn/go-sqlite3"
)

func openDB(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:selfmon_src?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkIncident 建一个最小 Incident（供业务通知关联）。
func mkIncident(t *testing.T, c *ent.Client, number string) *ent.Incident {
	t.Helper()
	inc, err := c.Incident.Create().
		SetNumber(number).SetTitle("t").
		SetSeverity(entincident.SeverityCritical).
		SetStatus(entincident.StatusTriggered).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// mkNotif 建一条送达记录。incID<=0 表示 unrouted（无 Incident 关联，模拟自监控/兜底告警）。
func mkNotif(t *testing.T, c *ent.Client, incID int, status entnotification.Status, createdAt time.Time) {
	t.Helper()
	b := c.Notification.Create().
		SetChannel("webhook").
		SetStatus(status).
		SetCreatedAt(createdAt)
	if incID > 0 {
		b = b.SetIncidentID(incID)
	}
	if _, err := b.Save(t.Context()); err != nil {
		t.Fatalf("create notification: %v", err)
	}
}

// TestFailureRateCountsBusinessOnly 只统计关联 Incident 的业务通知（sent+failed），
// 排除 unrouted（自监控/兜底）与 suppressed/pending。
func TestFailureRateCountsBusinessOnly(t *testing.T) {
	c := openDB(t)
	src := NewEntFailureRate(c)
	now := time.Now()
	inc := mkIncident(t, c, "INC-1")

	// 业务通知：3 failed + 2 sent（total=5, failed=3）。
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now)
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now)
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now)
	mkNotif(t, c, inc.ID, entnotification.StatusSent, now)
	mkNotif(t, c, inc.ID, entnotification.StatusSent, now)
	// suppressed / pending：不计入 total。
	mkNotif(t, c, inc.ID, entnotification.StatusSuppressed, now)
	mkNotif(t, c, inc.ID, entnotification.StatusPending, now)

	failed, total, err := src.Rate(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if total != 5 || failed != 3 {
		t.Fatalf("expected failed=3 total=5, got failed=%d total=%d", failed, total)
	}
}

// TestFailureRateExcludesUnrouted 防递归核心：unrouted 自监控告警（无 Incident）即便全失败，
// 也不得抬高业务失败率。
func TestFailureRateExcludesUnrouted(t *testing.T) {
	c := openDB(t)
	src := NewEntFailureRate(c)
	now := time.Now()
	inc := mkIncident(t, c, "INC-2")

	// 业务：1 sent（total=1, failed=0 → 0% 失败率）。
	mkNotif(t, c, inc.ID, entnotification.StatusSent, now)
	// 自监控/兜底：5 条全失败但 unrouted（incID=0）——必须被排除，否则会把失败率抬到 5/6。
	for i := 0; i < 5; i++ {
		mkNotif(t, c, 0, entnotification.StatusFailed, now)
	}

	failed, total, err := src.Rate(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if total != 1 || failed != 0 {
		t.Fatalf("unrouted self-mon failures must be excluded; expected failed=0 total=1, got failed=%d total=%d", failed, total)
	}
}

// TestFailureRateWindowFiltersOld 窗口外的旧记录不计入。
func TestFailureRateWindowFiltersOld(t *testing.T) {
	c := openDB(t)
	src := NewEntFailureRate(c)
	now := time.Now()
	inc := mkIncident(t, c, "INC-3")

	// 窗口内（近 15m）：2 failed。
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now.Add(-5*time.Minute))
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now.Add(-10*time.Minute))
	// 窗口外（1h 前）：不计。
	mkNotif(t, c, inc.ID, entnotification.StatusFailed, now.Add(-time.Hour))
	mkNotif(t, c, inc.ID, entnotification.StatusSent, now.Add(-time.Hour))

	failed, total, err := src.Rate(t.Context(), 15*time.Minute)
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if total != 2 || failed != 2 {
		t.Fatalf("window should keep only recent records; expected failed=2 total=2, got failed=%d total=%d", failed, total)
	}
}

// TestFailureRateEmpty 无记录时返回 (0,0)，不 panic、不除零。
func TestFailureRateEmpty(t *testing.T) {
	c := openDB(t)
	src := NewEntFailureRate(c)
	failed, total, err := src.Rate(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if failed != 0 || total != 0 {
		t.Fatalf("empty expected (0,0), got (%d,%d)", failed, total)
	}
}

// TestNilConstructors db/insp 为 nil 时构造器返回 nil（wire 侧据此降级）。
func TestNilConstructors(t *testing.T) {
	if NewEntFailureRate(nil) != nil {
		t.Fatal("NewEntFailureRate(nil) should return nil")
	}
	if NewInspectorQueueSource(nil) != nil {
		t.Fatal("NewInspectorQueueSource(nil) should return nil")
	}
}
