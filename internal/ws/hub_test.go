package ws

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kevin/vigil/ent"
	domainevent "github.com/kevin/vigil/internal/event"
)

// TestHub_SubscribeAndBroadcast 订阅后能收到广播。
func TestHub_SubscribeAndBroadcast(t *testing.T) {
	h := NewHub()
	c := newClient()
	unsub := h.Subscribe(1, c)
	defer unsub()

	h.BroadcastIncident(1, "ack", nil)

	select {
	case msg := <-c.send:
		var m Message
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != MsgIncidentChanged || m.IncidentID != 1 || m.Action != "ack" {
			t.Errorf("unexpected message: %+v", m)
		}
	default:
		t.Error("no message received after broadcast")
	}
}

// TestHub_UnsubscribeAfterUnsub 退订后不再收到广播。
func TestHub_UnsubscribeAfterUnsub(t *testing.T) {
	h := NewHub()
	c := newClient()
	unsub := h.Subscribe(1, c)
	unsub() // 立即退订

	h.BroadcastIncident(1, "ack", nil)

	select {
	case msg := <-c.send:
		t.Errorf("received message after unsubscribe: %s", msg)
	default:
		// 预期：无消息
	}
}

// TestHub_BroadcastOnlyToSubscribers 广播只发给订阅者，不发给其他 incident 的订阅者。
func TestHub_BroadcastOnlyToSubscribers(t *testing.T) {
	h := NewHub()
	c1 := newClient()
	c2 := newClient()
	defer h.Subscribe(1, c1)()
	defer h.Subscribe(2, c2)()

	h.BroadcastIncident(1, "ack", nil)

	// c1 收到
	select {
	case <-c1.send:
	default:
		t.Error("subscriber c1 received nothing")
	}
	// c2 不应收到（订阅的是 incident 2）
	select {
	case msg := <-c2.send:
		t.Errorf("non-subscriber c2 received: %s", msg)
	default:
	}
}

// TestHub_MultipleSubscribers 同一 incident 多订阅者都收到。
func TestHub_MultipleSubscribers(t *testing.T) {
	h := NewHub()
	c1 := newClient()
	c2 := newClient()
	c3 := newClient()
	defer h.Subscribe(1, c1)()
	defer h.Subscribe(1, c2)()
	defer h.Subscribe(1, c3)()

	h.BroadcastIncident(1, "resolve", nil)

	for i, c := range []*client{c1, c2, c3} {
		select {
		case <-c.send:
		default:
			t.Errorf("subscriber %d received nothing", i)
		}
	}
}

// TestHub_BroadcastNoSubscribers 无订阅者时静默跳过（不 panic）。
func TestHub_BroadcastNoSubscribers(t *testing.T) {
	h := NewHub()
	// 不应 panic
	h.BroadcastIncident(999, "ack", nil)
}

// TestHub_BroadcastTimelineAdded 验证 B11：时间线新增广播 timeline_added 消息给订阅者。
func TestHub_BroadcastTimelineAdded(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.Subscribe(7, c)()

	h.BroadcastTimelineAdded(7, map[string]any{"id": 1, "type": "status_changed"})

	select {
	case raw := <-c.send:
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != MsgTimelineAdded {
			t.Errorf("type: got %q, want %q", m.Type, MsgTimelineAdded)
		}
		if m.IncidentID != 7 {
			t.Errorf("incident_id: got %d, want 7", m.IncidentID)
		}
		if m.Data == nil {
			t.Error("timeline_added should carry item Data")
		}
	default:
		t.Error("no timeline_added message received after broadcast")
	}
}

// TestHub_UnsubscribeCleansUpMap 退订后 incident 的客户端集合被清理（防内存泄漏）。
func TestHub_UnsubscribeCleansUpMap(t *testing.T) {
	h := NewHub()
	c := newClient()
	unsub := h.Subscribe(1, c)
	unsub()

	h.mu.RLock()
	_, exists := h.clients[1]
	h.mu.RUnlock()
	if exists {
		t.Error("incident 1 still in clients map after unsubscribe (memory leak)")
	}
}

// TestHub_DashboardSubscribeAndBroadcast 看板订阅者收到 dashboard_update 广播。
func TestHub_DashboardSubscribeAndBroadcast(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.SubscribeDashboard(c)()

	h.BroadcastDashboard("resolve", dashboardSummary{IncidentID: 42, Status: "resolved"})

	select {
	case raw := <-c.send:
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != MsgDashboardUpdate {
			t.Errorf("type: got %q, want %q", m.Type, MsgDashboardUpdate)
		}
		if m.Action != "resolve" {
			t.Errorf("action: got %q, want resolve", m.Action)
		}
	default:
		t.Error("no dashboard_update received after broadcast")
	}
}

// TestHub_DashboardIsolatedFromIncident 看板订阅者不收 per-incident 广播，反之亦然
// （负键 topic 与真实正数 incident id 天然隔离，防两类订阅串台）。
func TestHub_DashboardIsolatedFromIncident(t *testing.T) {
	h := NewHub()
	dash := newClient()
	inc := newClient()
	defer h.SubscribeDashboard(dash)()
	defer h.Subscribe(1, inc)()

	// 只广播 incident 1 变更：incident 订阅者收到，看板订阅者不应收到。
	h.BroadcastIncident(1, "ack", nil)
	select {
	case <-inc.send: // 预期：incident 订阅者收到自己的广播
	default:
		t.Error("incident subscriber missed its own broadcast")
	}
	select {
	case msg := <-dash.send:
		t.Errorf("dashboard subscriber wrongly received incident broadcast: %s", msg)
	default:
	}
	// 只广播看板增量：看板订阅者收到，incident 订阅者不应收到。
	h.BroadcastDashboard("ack", nil)
	select {
	case <-dash.send: // 预期：看板订阅者收到看板增量
	default:
		t.Error("dashboard subscriber missed its own broadcast")
	}
	select {
	case msg := <-inc.send:
		t.Errorf("incident subscriber wrongly received dashboard broadcast: %s", msg)
	default:
	}
}

// TestHub_OnDashboardEvent 领域事件驱动看板广播：incident 事件 → dashboard_update 携带摘要。
func TestHub_OnDashboardEvent(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.SubscribeDashboard(c)()

	e := domainevent.Event{
		Type:     domainevent.IncidentResolved,
		Action:   domainevent.Action("resolve"),
		Incident: &ent.Incident{ID: 7, Number: "INC-7", Title: "db down"},
	}
	if err := h.OnDashboardEvent(context.Background(), e); err != nil {
		t.Fatalf("OnDashboardEvent: %v", err)
	}

	select {
	case raw := <-c.send:
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != MsgDashboardUpdate || m.Action != "resolve" {
			t.Errorf("unexpected: %+v", m)
		}
		// Data 应含触发单摘要。
		b, _ := json.Marshal(m.Data)
		if !json.Valid(b) || len(b) == 0 {
			t.Error("dashboard_update should carry summary Data")
		}
		var s dashboardSummary
		_ = json.Unmarshal(b, &s)
		if s.IncidentID != 7 || s.Number != "INC-7" {
			t.Errorf("summary mismatch: %+v", s)
		}
	default:
		t.Error("no dashboard_update after OnDashboardEvent")
	}
}

// TestHub_OnDashboardEventNilIncident 事件无 incident 时 no-op（不 panic、不广播）。
func TestHub_OnDashboardEventNilIncident(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.SubscribeDashboard(c)()

	if err := h.OnDashboardEvent(context.Background(), domainevent.Event{}); err != nil {
		t.Fatalf("OnDashboardEvent nil incident: %v", err)
	}
	select {
	case <-c.send:
		t.Error("should not broadcast when event has no incident")
	default:
	}
}

// TestHub_BroadcastDashboardTick 心跳广播 action=tick、Data=nil 给看板订阅者。
func TestHub_BroadcastDashboardTick(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.SubscribeDashboard(c)()

	h.BroadcastDashboardTick()
	select {
	case raw := <-c.send:
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != MsgDashboardUpdate || m.Action != "tick" {
			t.Errorf("tick unexpected: %+v", m)
		}
	default:
		t.Error("no tick received")
	}
}

// TestHub_SendBufferFullSkipsSlowClient 客户端 send 缓冲满时广播不阻塞（跳过慢客户端）。
func TestHub_SendBufferFullSkipsSlowClient(t *testing.T) {
	h := NewHub()
	c := newClient()
	defer h.Subscribe(1, c)()

	// 填满 send 缓冲（16 条），再多广播几条应被跳过而非阻塞
	for i := 0; i < 30; i++ {
		h.BroadcastIncident(1, "ack", nil)
	}
	// 能到这里说明没阻塞（Broadcast 用 select default 跳过了满缓冲）
}
