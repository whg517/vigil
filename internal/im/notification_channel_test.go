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

// —— QA 审计 C5：通知主路径卡片必须带操作按钮 ——

// TestIMChannel_SendRendersButtons_NoRenderer 无渲染器时卡片渲染全部默认按钮
// （宽松渲染策略，回调 resolveAndCheck 兜底鉴权）。修复前主路径卡片零按钮。
func TestIMChannel_SendRendersButtons_NoRenderer(t *testing.T) {
	reg := NewRegistry()
	bot := newStubBot("feishu", true)
	reg.Register(bot)
	ch := NewIMChannel(reg, NewCardStore(), func(*ent.Incident, []notification.Target) string { return "g" })

	_, err := ch.Send(context.Background(), &notification.Message{
		Incident: newIncidentForTest(),
		Targets:  []notification.Target{{UserID: 1, Name: "张三", Source: "schedule"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(bot.sentCards) != 1 {
		t.Fatalf("sentCards: got %d, want 1", len(bot.sentCards))
	}
	// C5 关键断言：卡片必须带操作按钮（修复前为 0）
	if len(bot.sentCards[0].Buttons) == 0 {
		t.Fatal("C5 回归：通知主路径卡片零按钮，值班人无法在 IM 内 ack/升级/解决")
	}
}

// TestIMChannel_SendRendersButtons_WithRenderer 有渲染器时按接收者权限裁剪按钮。
func TestIMChannel_SendRendersButtons_WithRenderer(t *testing.T) {
	reg := NewRegistry()
	bot := newStubBot("feishu", true)
	reg.Register(bot)
	// 渲染器：user 1 只有 ack 权限，无 escalate/resolve
	renderer := NewRenderer(func(userID int, teamScope *int, perms []string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, p := range perms {
			out[p] = p == "incident.ack" // 仅 ack 放行
		}
		return out, nil
	})
	ch := NewIMChannel(reg, NewCardStore(), func(*ent.Incident, []notification.Target) string { return "g" })
	ch.SetRenderer(renderer)

	_, err := ch.Send(context.Background(), &notification.Message{
		Incident: newIncidentForTest(),
		Targets:  []notification.Target{{UserID: 1, Name: "张三", Source: "schedule"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(bot.sentCards) != 1 {
		t.Fatalf("sentCards: got %d, want 1", len(bot.sentCards))
	}
	// 应只有 ack 按钮（escalate/resolve 被权限裁剪掉）
	btns := bot.sentCards[0].Buttons
	if len(btns) != 1 || btns[0].Value != ActionAck {
		t.Fatalf("权限感知裁剪后应只剩 ack 按钮，got %+v", btns)
	}
}
