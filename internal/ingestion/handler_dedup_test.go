// handler_dedup_test.go 验证 QA 审计 C2：firing 与 resolved 共用同一 fingerprint 时
// 两条 Event 都能落库（旧唯一索引 (source, source_event_id) 会丢弃 resolved）。
package ingestion

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/event"

	_ "github.com/mattn/go-sqlite3"
)

// newDedupTestDB 构造内存库 + prometheus integration。
func newDedupTestDB(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t1").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s1").SetTeamID(team.ID).Save(ctx)
	_, _ = c.Integration.Create().
		SetName("prom").SetType("prometheus").SetToken("dedup-token").
		SetTeamID(team.ID).SetServiceID(svc.ID).
		Save(ctx)
	return c
}

// TestEventUniqueIndex_FiringResolvedCoexist 验证 C2：相同 (source, source_event_id)
// 下 firing 与 resolved 两条 Event 都能落库（status 不同）。
// 这是审计 C2 的回归守护：若唯一索引退回不含 status，此测试会失败。
func TestEventUniqueIndex_FiringResolvedCoexist(t *testing.T) {
	c := newDedupTestDB(t, "dedup_coexist")
	ctx := context.Background()

	// firing 事件
	_, err := c.Event.Create().
		SetSourceEventID("fp-shared").
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusFiring).
		SetSummary("告警触发").
		SetDedupKey("prometheus:fp-shared").
		Save(ctx)
	if err != nil {
		t.Fatalf("create firing event: %v", err)
	}

	// resolved 事件：相同 source_event_id，不同 status
	_, err = c.Event.Create().
		SetSourceEventID("fp-shared").
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusResolved).
		SetSummary("告警恢复").
		SetDedupKey("prometheus:fp-shared").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resolved event (C2 regression): %v "+
			"(unique index must include status so firing+resolved coexist)", err)
	}

	// 两条都应在库
	cnt, err := c.Event.Query().
		Where(event.SourceEQ("prometheus"), event.SourceEventIDEQ("fp-shared")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 2 {
		t.Errorf("event count=%d, want 2 (firing + resolved)", cnt)
	}
}

// TestEventUniqueIndex_DuplicateFiringStillDeduped 验证索引修复不破坏幂等：
// 完全相同（含 status=firing）的重复推送仍被唯一约束拒绝（幂等去重仍生效）。
func TestEventUniqueIndex_DuplicateFiringStillDeduped(t *testing.T) {
	c := newDedupTestDB(t, "dedup_idem")
	ctx := context.Background()

	create := func() error {
		_, err := c.Event.Create().
			SetSourceEventID("fp-x").
			SetSource("prometheus").
			SetSeverity(event.SeverityWarning).
			SetStatus(event.StatusFiring).
			SetSummary("dup").
			SetDedupKey("prometheus:fp-x").
			Save(ctx)
		return err
	}
	if err := create(); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := create()
	if !ent.IsConstraintError(err) {
		t.Fatalf("duplicate firing should be rejected by unique index (idempotency), got: %v", err)
	}
}
