// targeted_test.go T4.4 定向订阅通知（NotifyTargeted）行为测试：
//   - 定向通知走降级链 + 送达记录（复用 T2.2）
//   - 订阅者一律非值班人：quiet_hours 抑制非 critical → 记 suppressed
//   - critical 穿透 quiet_hours（照发不误抑制）
package notification

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	entnotification "github.com/kevin/vigil/ent/notification"
)

// TestNotifyTargeted_DeliversAndRecords T4.4：定向通知送达并落送达记录。
func TestNotifyTargeted_DeliversAndRecords(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))

	err := n.NotifyTargeted(ctx, inc, []Target{{UserID: 7, Name: "leader", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyTargeted: %v", err)
	}
	if email.calls == 0 {
		t.Error("email channel should be used for targeted notification")
	}
	sent, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusSent)).Count(ctx)
	if sent == 0 {
		t.Error("expected a sent delivery record for targeted subscriber")
	}
}

// TestNotifyTargeted_QuietHoursSuppressNonCritical T4.4：非值班订阅者夜间非 critical 被静默 → 记 suppressed。
func TestNotifyTargeted_QuietHoursSuppressNonCritical(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))
	// always-on 静默窗（00:00-23:59）：与真实时钟无关，warning 恒落窗内 → 订阅者非值班人应被抑制。
	n.SetQuietHoursResolver(func(_ *ent.Incident) *QuietHours {
		return &QuietHours{Enabled: true, Start: "00:00", End: "23:59", Timezone: "UTC"}
	})

	err := n.NotifyTargeted(ctx, inc, []Target{{UserID: 7, Name: "leader", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyTargeted: %v", err)
	}
	if email.calls != 0 {
		t.Error("non-critical night notification to subscriber should be suppressed (not sent)")
	}
	suppressed, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusSuppressed)).Count(ctx)
	if suppressed == 0 {
		t.Error("expected a suppressed delivery record for quiet-hours subscriber")
	}
}

// TestNotifyTargeted_CriticalBypassQuietHours T4.4：critical 穿透静默，即使订阅者非值班、在静默窗内也照发。
func TestNotifyTargeted_CriticalBypassQuietHours(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	reg.Register(email)
	n := NewNotifier(reg, []string{"email"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))
	n.SetQuietHoursResolver(func(_ *ent.Incident) *QuietHours {
		return &QuietHours{Enabled: true, Start: "00:00", End: "23:59", Timezone: "UTC"}
	})

	err := n.NotifyTargeted(ctx, inc, []Target{{UserID: 7, Name: "leader", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyTargeted: %v", err)
	}
	if email.calls == 0 {
		t.Error("critical should bypass quiet_hours and be delivered to subscriber")
	}
}
