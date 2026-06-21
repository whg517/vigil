package ws

import (
	"encoding/json"
	"testing"
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
