package timeline

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/timelineitem"
)

// fakeBroadcaster 捕获 BroadcastTimelineAdded 调用（B11 测试用）。
type fakeBroadcaster struct {
	calls []struct {
		incidentID int
		item       any
	}
}

func (f *fakeBroadcaster) BroadcastTimelineAdded(incidentID int, item any) {
	f.calls = append(f.calls, struct {
		incidentID int
		item       any
	}{incidentID, item})
}

// TestRecorder_BroadcastsTimelineAdded 验证 B11：写入时间线成功后触发 timeline_added 广播，
// 且携带 incident_id 与刚写入的条目。
func TestRecorder_BroadcastsTimelineAdded(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)
	fb := &fakeBroadcaster{}
	r.SetBroadcaster(fb)

	if err := r.Record(context.Background(), inc.ID, timelineitem.TypeStatusChanged,
		"状态变更", Actor{Kind: "system"}, timelineitem.SourceSystem,
		map[string]any{"status": "resolved"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if len(fb.calls) != 1 {
		t.Fatalf("broadcast calls: got %d, want 1", len(fb.calls))
	}
	if fb.calls[0].incidentID != inc.ID {
		t.Errorf("broadcast incident_id: got %d, want %d", fb.calls[0].incidentID, inc.ID)
	}
	item, ok := fb.calls[0].item.(*ent.TimelineItem)
	if !ok || item == nil {
		t.Fatalf("broadcast item: got %T, want *ent.TimelineItem", fb.calls[0].item)
	}
	if item.Type != timelineitem.TypeStatusChanged {
		t.Errorf("broadcast item type: got %q, want status_changed", item.Type)
	}
}

// TestRecorder_NoBroadcastOnWriteFailure 验证写入失败（空 content）不触发广播。
func TestRecorder_NoBroadcastOnWriteFailure(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c)
	r := NewRecorder(c)
	fb := &fakeBroadcaster{}
	r.SetBroadcaster(fb)

	_ = r.Record(context.Background(), inc.ID, timelineitem.TypeNoteAdded,
		"", Actor{Kind: "user"}, timelineitem.SourceWeb, nil)

	if len(fb.calls) != 0 {
		t.Errorf("write failure should not broadcast, got %d calls", len(fb.calls))
	}
}
