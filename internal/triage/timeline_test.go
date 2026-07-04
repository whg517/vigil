package triage

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// TestEngine_IncidentCreated_WritesTimeline 验证：建单时写 incident_created 时间线（B4）。
// 原先该类型零写入——建单是「全程留痕」起点，必须留痕。
func TestEngine_IncidentCreated_WritesTimeline(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)
	eng.SetRecorder(timeline.NewRecorder(c))

	evt := createEvent(t, c, event.SeverityWarning, "k1")
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("Action: got %q, want incident_created", res.Action)
	}

	items, err := c.TimelineItem.Query().
		Where(
			timelineitem.HasIncidentWith(incident.IDEQ(res.IncidentID)),
			timelineitem.TypeEQ(timelineitem.TypeIncidentCreated),
		).All(context.Background())
	if err != nil {
		t.Fatalf("query timeline: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("incident_created timeline count: got %d, want 1", len(items))
	}
	it := items[0]
	if it.Source != timelineitem.SourceSystem {
		t.Errorf("source: got %q, want system", it.Source)
	}
	if it.Actor["kind"] != "system" {
		t.Errorf("actor.kind: got %q, want system", it.Actor["kind"])
	}
}

// TestEngine_EventAttached_WritesTimeline 验证：聚合并入既有 Incident 时写 event_attached 时间线（B5）。
// 原先该类型零写入——复盘看不到「后续并入了哪些告警」。
func TestEngine_EventAttached_WritesTimeline(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)
	eng.SetRecorder(timeline.NewRecorder(c))

	// 首条建单
	evt1 := createEvent(t, c, event.SeverityWarning, "k1")
	res1, err := eng.Process(context.Background(), evt1.ID)
	if err != nil {
		t.Fatalf("Process evt1: %v", err)
	}
	// 次条并入
	evt2 := createEvent(t, c, event.SeverityWarning, "k2")
	res2, err := eng.Process(context.Background(), evt2.ID)
	if err != nil {
		t.Fatalf("Process evt2: %v", err)
	}
	if res2.Action != ActionAggregated {
		t.Fatalf("evt2 Action: got %q, want aggregated", res2.Action)
	}

	items, err := c.TimelineItem.Query().
		Where(
			timelineitem.HasIncidentWith(incident.IDEQ(res1.IncidentID)),
			timelineitem.TypeEQ(timelineitem.TypeEventAttached),
		).All(context.Background())
	if err != nil {
		t.Fatalf("query timeline: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("event_attached timeline count: got %d, want 1", len(items))
	}
	if items[0].Source != timelineitem.SourceSystem {
		t.Errorf("source: got %q, want system", items[0].Source)
	}
	// detail 应带并入的 event_id，供复盘追溯到具体告警。
	if _, ok := items[0].Detail["event_id"]; !ok {
		t.Errorf("detail should carry event_id, got %v", items[0].Detail)
	}
}

// TestEngine_NoRecorder_NoTimeline 验证：未注入 recorder 时不 panic、不写时间线（降级）。
func TestEngine_NoRecorder_NoTimeline(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil) // 不 SetRecorder

	evt := createEvent(t, c, event.SeverityWarning, "k1")
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	n, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(res.IncidentID))).
		Count(context.Background())
	if n != 0 {
		t.Errorf("no recorder should write 0 timeline items, got %d", n)
	}
}
