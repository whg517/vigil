//go:build integration

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
)

// 默认轮询参数：流水线（队列消费/升级延迟）通常秒级完成，留足余量。
const (
	defaultTimeout  = 15 * time.Second
	defaultInterval = 200 * time.Millisecond
)

// Eventually 反复调用 condition 直到返回 true 或超时。
// 用于等待异步流水线（队列消费、升级任务触发）产生的副作用可见。
func Eventually(t *testing.T, desc string, condition func() bool) bool {
	t.Helper()
	return EventuallyIn(t, desc, defaultTimeout, defaultInterval, condition)
}

// EventuallyIn 带自定义超时/间隔的 Eventually。
func EventuallyIn(t *testing.T, desc string, timeout, interval time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(interval)
	}
	if condition() {
		return true
	}
	t.Fatalf("eventually: %s (timed out after %v)", desc, timeout)
	return false
}

// WaitForIncidentCount 轮询直到库中有 count 条 incident（用于验证流水线建单）。
func (e *Env) WaitForIncidentCount(t *testing.T, count int) []*ent.Incident {
	t.Helper()
	var list []*ent.Incident
	Eventually(t, "wait for incident count", func() bool {
		incs, err := e.DB().Incident.Query().Order(ent.Asc(incident.FieldID)).All(context.Background())
		if err != nil || len(incs) != count {
			return false
		}
		list = incs
		return true
	})
	return list
}

// WaitForIncidentStatus 轮询直到指定 incident 达到目标状态。
func (e *Env) WaitForIncidentStatus(t *testing.T, incID int, want incident.Status) *ent.Incident {
	t.Helper()
	var got *ent.Incident
	Eventually(t, "wait for incident "+itoa(incID)+" status="+string(want), func() bool {
		inc, err := e.DB().Incident.Get(context.Background(), incID)
		if err != nil || inc.Status != want {
			return false
		}
		got = inc
		return true
	})
	return got
}

// WaitForEscalationLevel 轮询直到 incident 的 current_level 达到目标层级。
// 验证 Asynq 延迟任务驱动的升级链是否如期推进。
func (e *Env) WaitForEscalationLevel(t *testing.T, incID, wantLevel int) *ent.Incident {
	t.Helper()
	var got *ent.Incident
	Eventually(t, "wait for escalation level "+itoa(incID)+"→"+itoa(wantLevel), func() bool {
		inc, err := e.DB().Incident.Get(context.Background(), incID)
		if err != nil || inc.CurrentLevel != wantLevel {
			return false
		}
		got = inc
		return true
	})
	return got
}

// WaitForTimelineEntry 轮询直到 incident 有至少一条时间线条目。
// 验证 escalation/runbook 等是否正确记录了操作痕迹。
func (e *Env) WaitForTimelineEntry(t *testing.T, incID int) {
	t.Helper()
	Eventually(t, "wait for timeline entry on incident "+itoa(incID), func() bool {
		cnt, err := e.DB().TimelineItem.Query().
			Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
			Count(context.Background())
		if err != nil {
			return false
		}
		return cnt > 0
	})
}

// itoa 简单整数转字符串（避免引入 strconv 仅为此）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	const digits = "0123456789"
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
