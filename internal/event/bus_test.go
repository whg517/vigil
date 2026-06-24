package event

import (
	"context"
	"errors"
	"testing"
)

// TestPublish_NoSubscribers 无订阅者时 Publish 为安全 no-op。
func TestPublish_NoSubscribers(t *testing.T) {
	b := New()
	b.Publish(context.Background(), Event{Type: IncidentAcked})
}

// TestSubscribe_MultipleHandlersFanOut 同一 Type 多订阅者全部被调用，按订阅顺序。
func TestSubscribe_MultipleHandlersFanOut(t *testing.T) {
	b := New()
	var order []int
	b.Subscribe(IncidentAcked, func(_ context.Context, _ Event) error {
		order = append(order, 1)
		return nil
	})
	b.Subscribe(IncidentAcked, func(_ context.Context, _ Event) error {
		order = append(order, 2)
		return nil
	})
	b.Subscribe(IncidentAcked, func(_ context.Context, _ Event) error {
		order = append(order, 3)
		return nil
	})

	b.Publish(context.Background(), Event{Type: IncidentAcked})

	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Errorf("fan-out order = %v, want [1 2 3]", order)
	}
}

// TestPublish_HandlerErrorDoesNotStopOthers 单个订阅者返回 error 不中断后续订阅者。
func TestPublish_HandlerErrorDoesNotStopOthers(t *testing.T) {
	b := New()
	var called bool
	b.Subscribe(IncidentResolved, func(_ context.Context, _ Event) error {
		return errors.New("boom")
	})
	b.Subscribe(IncidentResolved, func(_ context.Context, _ Event) error {
		called = true
		return nil
	})

	b.Publish(context.Background(), Event{Type: IncidentResolved})

	if !called {
		t.Error("second handler not called after first returned error")
	}
}

// TestPublish_HandlerPanicDoesNotStopOthers 单个订阅者 panic 不中断后续订阅者。
func TestPublish_HandlerPanicDoesNotStopOthers(t *testing.T) {
	b := New()
	var called bool
	b.Subscribe(IncidentResolved, func(_ context.Context, _ Event) error {
		panic("kaboom")
	})
	b.Subscribe(IncidentResolved, func(_ context.Context, _ Event) error {
		called = true
		return nil
	})

	b.Publish(context.Background(), Event{Type: IncidentResolved})

	if !called {
		t.Error("second handler not called after first panicked")
	}
}

// TestPublish_OnlyMatchingTypeDispatched 不匹配的 Type 不被调用。
func TestPublish_OnlyMatchingTypeDispatched(t *testing.T) {
	b := New()
	var ackCalled, resolveCalled bool
	b.Subscribe(IncidentAcked, func(_ context.Context, _ Event) error { ackCalled = true; return nil })
	b.Subscribe(IncidentResolved, func(_ context.Context, _ Event) error { resolveCalled = true; return nil })

	b.Publish(context.Background(), Event{Type: IncidentAcked})

	if !ackCalled {
		t.Error("acked handler not called for IncidentAcked")
	}
	if resolveCalled {
		t.Error("resolved handler called for IncidentAcked (should not)")
	}
}

// TestSubscribe_ConcurrentSafe Subscribe 与 Publish 并发不竞争（-race 覆盖）。
func TestSubscribe_ConcurrentSafe(t *testing.T) {
	b := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			b.Subscribe(IncidentAcked, func(_ context.Context, _ Event) error { return nil })
		}
	}()
	for i := 0; i < 100; i++ {
		b.Publish(context.Background(), Event{Type: IncidentAcked})
	}
	<-done
}
