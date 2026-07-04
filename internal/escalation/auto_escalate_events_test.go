package escalation

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	domainevent "github.com/kevin/vigil/internal/event"

	"github.com/hibiken/asynq"
)

// TestHandleTask_PublishesEscalatedEvent 验证 B10：
// 自动升级（计时器到点触发 HandleTask）发布 IncidentEscalated 领域事件，
// 且标记 SystemTriggered=true（供 OnManualEscalate 跳过，避免反馈环），
// 携带最新 incident 快照 + level 索引。
func TestHandleTask_PublishesEscalatedEvent(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	bus := domainevent.New()
	var captured []domainevent.Event
	bus.Subscribe(domainevent.IncidentEscalated, func(_ context.Context, e domainevent.Event) error {
		captured = append(captured, e)
		return nil
	})
	env.engine.SetBus(bus)

	env.runEscTask(t, id, 0, 0)

	if len(captured) != 1 {
		t.Fatalf("captured IncidentEscalated events: got %d, want 1", len(captured))
	}
	ev := captured[0]
	if ev.Incident == nil || ev.Incident.ID != id {
		t.Errorf("event incident: got %+v, want id=%d", ev.Incident, id)
	}
	if ev.Incident.Status != incident.StatusEscalated {
		t.Errorf("event incident status: got %q, want escalated", ev.Incident.Status)
	}
	if ev.Level != 0 {
		t.Errorf("event level: got %d, want 0", ev.Level)
	}
	if !ev.SystemTriggered {
		t.Error("auto-escalation event should be SystemTriggered=true")
	}
	if string(ev.Action) != "escalate" {
		t.Errorf("event action: got %q, want escalate", ev.Action)
	}
}

// TestOnManualEscalate_SkipsSystemTriggered 验证反馈环防护：
// SystemTriggered=true 的升级事件不再触发 TriggerLevelNow（否则自动升级会死循环）；
// 非系统触发（手动）事件仍正常入队。
func TestOnManualEscalate_SkipsSystemTriggered(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	inc, err := env.client.Incident.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}

	if err := env.engine.OnManualEscalate(context.Background(), domainevent.Event{
		Type:            domainevent.IncidentEscalated,
		Incident:        inc,
		Level:           0,
		SystemTriggered: true,
	}); err != nil {
		t.Fatalf("OnManualEscalate: %v", err)
	}

	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	pending, _ := inspector.ListPendingTasks("critical", asynq.PageSize(10))
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	if len(pending)+len(scheduled) != 0 {
		t.Errorf("system-triggered escalate should not enqueue, got %d pending + %d scheduled",
			len(pending), len(scheduled))
	}

	if err := env.engine.OnManualEscalate(context.Background(), domainevent.Event{
		Type:            domainevent.IncidentEscalated,
		Incident:        inc,
		Level:           0,
		SystemTriggered: false,
	}); err != nil {
		t.Fatalf("OnManualEscalate manual: %v", err)
	}
	pending2, _ := inspector.ListPendingTasks("critical", asynq.PageSize(10))
	if len(pending2) != 1 {
		t.Errorf("manual escalate should enqueue 1 task, got %d", len(pending2))
	}
}
