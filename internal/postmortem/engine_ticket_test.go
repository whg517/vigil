// engine_ticket_test.go 复盘发布 → 工单联动 best-effort 契约测试（T4.3）。
//
// 锁定：
//   - 配了 TicketCreator 时，发布触发 OnPostmortemPublished（建单回填）。
//   - TicketCreator 内部失败（工单系统不可达）时，Transition 仍成功发布（best-effort 不阻断）。
package postmortem

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/postmortem"

	_ "github.com/mattn/go-sqlite3"
)

// fakeTicketCreator 记录是否被调用；panicOnCall 用于验证 best-effort 隔离（引擎须容忍其内部失败）。
type fakeTicketCreator struct {
	calledPMID int
	backfill   func(ctx context.Context, db *ent.Client, pmID int)
	db         *ent.Client
}

func (f *fakeTicketCreator) OnPostmortemPublished(ctx context.Context, pmID int) {
	f.calledPMID = pmID
	if f.backfill != nil {
		f.backfill(ctx, f.db, pmID)
	}
}

func setupPublishablePM(t *testing.T) (*ent.Client, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:pm_ticket_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	inc := c.Incident.Create().SetNumber("INC-" + t.Name()).SetTitle("t").
		SetSeverity("critical").SetStatus("resolved").SaveX(ctx)
	pm := c.Postmortem.Create().SetIncidentID(inc.ID).SetStatus("in_review").
		SetGeneratedBy("human").SetSections(map[string]any{}).SaveX(ctx)
	return c, pm.ID
}

// TestPublish_TriggersTicketCreation 发布复盘触发工单联动（TicketCreator 被调用，可回填 tracker_url）。
func TestPublish_TriggersTicketCreation(t *testing.T) {
	c, pmID := setupPublishablePM(t)
	ctx := context.Background()
	ai := c.ActionItem.Create().SetDescription("补监控").SetPostmortemID(pmID).SaveX(ctx)

	tc := &fakeTicketCreator{db: c, backfill: func(ctx context.Context, db *ent.Client, pmID int) {
		// 模拟建单回填 tracker_url。
		db.ActionItem.UpdateOneID(ai.ID).SetTrackerURL("https://tk/T-1").ExecX(ctx)
	}}
	eng := NewEngine(c, nil)
	eng.SetTicketCreator(tc)

	pm, err := eng.Transition(ctx, pmID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pm.Status != postmortem.StatusPublished {
		t.Fatalf("status: got %s, want published", pm.Status)
	}
	if tc.calledPMID != pmID {
		t.Errorf("TicketCreator not called with pmID: got %d, want %d", tc.calledPMID, pmID)
	}
	if got := c.ActionItem.GetX(ctx, ai.ID); got.TrackerURL != "https://tk/T-1" {
		t.Errorf("tracker_url not backfilled: got %q", got.TrackerURL)
	}
}

// noopFailCreator 模拟工单系统不可达：真实 ticket.Engine 内部吞错、什么也不回填。
// 用无副作用桩验证「联动失败也不影响发布」。
type noopFailCreator struct{ called bool }

func (n *noopFailCreator) OnPostmortemPublished(_ context.Context, _ int) {
	n.called = true // 内部已吞错（工单系统不可达），不回填 tracker_url。
}

// TestPublish_TicketFailure_DoesNotBlock 工单系统不可达（联动无回填）时，复盘仍成功发布。
func TestPublish_TicketFailure_DoesNotBlock(t *testing.T) {
	c, pmID := setupPublishablePM(t)
	ctx := context.Background()
	ai := c.ActionItem.Create().SetDescription("补监控").SetPostmortemID(pmID).SaveX(ctx)

	nf := &noopFailCreator{}
	eng := NewEngine(c, nil)
	eng.SetTicketCreator(nf)

	pm, err := eng.Transition(ctx, pmID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("publish must succeed despite ticket failure: %v", err)
	}
	if pm.Status != postmortem.StatusPublished {
		t.Errorf("status: got %s, want published", pm.Status)
	}
	if !nf.called {
		t.Error("TicketCreator should have been invoked on publish")
	}
	// 建单失败：tracker_url 保持空（发布不因此回滚/阻断）。
	if got := c.ActionItem.GetX(ctx, ai.ID); got.TrackerURL != "" {
		t.Errorf("failed ticket creation should leave tracker_url empty, got %q", got.TrackerURL)
	}
}

// TestPublish_NoTicketCreator_NoOp 未注入 TicketCreator 时发布正常（不联动，不报错）。
func TestPublish_NoTicketCreator_NoOp(t *testing.T) {
	c, pmID := setupPublishablePM(t)
	ctx := context.Background()
	pm, err := NewEngine(c, nil).Transition(ctx, pmID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("publish without ticket creator: %v", err)
	}
	if pm.Status != postmortem.StatusPublished {
		t.Errorf("status: got %s", pm.Status)
	}
}
