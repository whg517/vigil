package im

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/notification"
)

// newIncidentForTest 建一个内存 incident（不入库，仅用于 BuildCard）。
func newIncidentForTest() *ent.Incident {
	return &ent.Incident{
		ID:       42,
		Number:   "INC-0042",
		Title:    "支付5xx",
		Severity: "critical",
		Status:   "triggered",
		Summary:  "支付服务5xx错误率超阈值",
	}
}

// TestIMChannel_Name 验证通道名。
func TestIMChannel_Name(t *testing.T) {
	c := NewIMChannel(NewRegistry(), NewCardStore(), nil)
	if c.Name() != "im" {
		t.Errorf("Name: got %q, want im", c.Name())
	}
}

// TestIMChannel_Send 验证通过可用 bot 发送卡片 + 记录 CardStore。
func TestIMChannel_Send(t *testing.T) {
	reg := NewRegistry()
	bot := newStubBot("feishu", true)
	reg.Register(bot)
	cards := NewCardStore()

	ch := NewIMChannel(reg, cards, func(*ent.Incident, []notification.Target) string {
		return "oc_test_group" // 模拟值班群
	})

	msg := &notification.Message{
		Incident: newIncidentForTest(),
		Targets:  []notification.Target{{UserID: 1, Name: "张三", Source: "schedule"}},
		Level:    0,
	}
	results, err := ch.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected 1 success, got %+v", results)
	}
	// bot 应被调用发送
	if bot.sendCount != 1 {
		t.Errorf("bot sendCount: got %d, want 1", bot.sendCount)
	}
	// CardStore 应记录卡片 ID
	if _, ok := cards.Get(42, "feishu"); !ok {
		t.Error("CardStore should record card after send")
	}
}

// TestIMChannel_NoChannel 验证无目标 channel 时不发送（非错误）。
func TestIMChannel_NoChannel(t *testing.T) {
	reg := NewRegistry()
	bot := newStubBot("feishu", true)
	reg.Register(bot)

	ch := NewIMChannel(reg, NewCardStore(), func(*ent.Incident, []notification.Target) string {
		return "" // 无 channel
	})
	results, err := ch.Send(context.Background(), &notification.Message{Incident: newIncidentForTest()})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("无 channel 时不应发送，got %d results", len(results))
	}
	if bot.sendCount != 0 {
		t.Error("bot should not be called when no channel")
	}
}

// TestIMChannel_NoIncident 验证无 incident 报错。
func TestIMChannel_NoIncident(t *testing.T) {
	ch := NewIMChannel(NewRegistry(), NewCardStore(), func(*ent.Incident, []notification.Target) string { return "x" })
	_, err := ch.Send(context.Background(), &notification.Message{Incident: nil})
	if err == nil {
		t.Error("无 incident 应报错")
	}
}

// TestIMChannel_FulfillsInterface 编译期验证实现 notification.Channel。
func TestIMChannel_FulfillsInterface(t *testing.T) {
	var _ notification.Channel = (*IMChannel)(nil)
}
