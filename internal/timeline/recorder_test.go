package timeline

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/runbook"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:tl_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func seedIncident(t *testing.T, c *ent.Client) *ent.Incident {
	t.Helper()
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").SetTitle("测试").SetSeverity("warning").
		SetStatus("triggered").SetPriority("p2").SetSummary("测试事件").
		SetTriggerType("auto").Save(context.Background())
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// TestRecorder_Record 验证写入时间线。
func TestRecorder_Record(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)

	err := r.Record(context.Background(), inc.ID, timelineitem.TypeIncidentCreated,
		"事件创建", Actor{Kind: "system"}, timelineitem.SourceSystem,
		map[string]any{"source_event": "evt-1"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// 验证写入
	items, _ := r.Query(context.Background(), inc.ID, "", "", 10, 0)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Content != "事件创建" {
		t.Errorf("content: got %q", items[0].Content)
	}
}

// TestRecorder_RecordEmptyContent 验证空 content 被拒。
func TestRecorder_RecordEmptyContent(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)

	err := r.Record(context.Background(), inc.ID, timelineitem.TypeNoteAdded,
		"", Actor{Kind: "user"}, timelineitem.SourceWeb, nil)
	if err == nil {
		t.Error("empty content should be rejected")
	}
}

// TestRecorder_QueryFilter 验证按 type/source 筛选。
func TestRecorder_QueryFilter(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)
	ctx := context.Background()

	// 写三条不同类型/来源
	_ = r.Record(ctx, inc.ID, timelineitem.TypeIncidentCreated, "a", Actor{Kind: "system"}, timelineitem.SourceSystem, nil)
	_ = r.Record(ctx, inc.ID, timelineitem.TypeEscalated, "b", Actor{Kind: "system"}, timelineitem.SourceSystem, nil)
	_ = r.Record(ctx, inc.ID, timelineitem.TypeNoteAdded, "c", Actor{Kind: "user"}, timelineitem.SourceWeb, nil)

	// 按 type=escalated 筛
	items, _ := r.Query(ctx, inc.ID, timelineitem.TypeEscalated, "", 10, 0)
	if len(items) != 1 || items[0].Content != "b" {
		t.Errorf("type filter: got %+v", items)
	}
	// 按 source=web 筛
	items, _ = r.Query(ctx, inc.ID, "", timelineitem.SourceWeb, 10, 0)
	if len(items) != 1 || items[0].Content != "c" {
		t.Errorf("source filter: got %+v", items)
	}
	// 不筛
	items, _ = r.Query(ctx, inc.ID, "", "", 10, 0)
	if len(items) != 3 {
		t.Errorf("no filter: got %d items", len(items))
	}
}

// TestRecorder_QueryOrder 验证按时间正序返回。
func TestRecorder_QueryOrder(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)
	ctx := context.Background()

	// 故意乱序写，靠 timestamp 排序
	_ = r.Record(ctx, inc.ID, timelineitem.TypeNoteAdded, "third",
		Actor{Kind: "user"}, timelineitem.SourceWeb, nil)
	// 略微延时保证 timestamp 不同
	time.Sleep(5 * time.Millisecond)
	_ = r.Record(ctx, inc.ID, timelineitem.TypeNoteAdded, "fourth",
		Actor{Kind: "user"}, timelineitem.SourceWeb, nil)

	items, _ := r.Query(ctx, inc.ID, "", "", 10, 0)
	if len(items) < 2 {
		t.Fatalf("expected >=2 items")
	}
	// 最后写的应在后（正序）
	if items[len(items)-1].Content != "fourth" {
		t.Errorf("expected last=fourth, got %q", items[len(items)-1].Content)
	}
}

// TestRecorder_Pagination 验证分页。
func TestRecorder_Pagination(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = r.Record(ctx, inc.ID, timelineitem.TypeNoteAdded, "n",
			Actor{Kind: "user"}, timelineitem.SourceWeb, nil)
	}
	total, _ := r.Count(ctx, inc.ID)
	if total != 5 {
		t.Errorf("count: got %d, want 5", total)
	}
	// limit=2 offset=1
	items, _ := r.Query(ctx, inc.ID, "", "", 2, 1)
	if len(items) != 2 {
		t.Errorf("page: got %d items, want 2", len(items))
	}
}

// TestRecorder_FulfillsRunbookInterface 验证 Recorder 实现 runbook.TimelineRecorder。
func TestRecorder_FulfillsRunbookInterface(t *testing.T) {
	var _ runbook.TimelineRecorder = (*Recorder)(nil)
}

// TestRecorder_RecordRunbook 验证 RecordRunbook 写入。
func TestRecorder_RecordRunbook(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)

	if err := r.RecordRunbook(context.Background(), inc.ID, "查日志", "ok", true); err != nil {
		t.Fatalf("RecordRunbook: %v", err)
	}
	items, _ := r.Query(context.Background(), inc.ID, timelineitem.TypeRunbookExecuted, "", 10, 0)
	if len(items) != 1 {
		t.Fatalf("expected 1 runbook item, got %d", len(items))
	}
}
