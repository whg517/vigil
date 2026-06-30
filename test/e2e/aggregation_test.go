//go:build integration

// aggregation_test.go 通知聚合 e2e（QA 审计 C3）。
//
// 审计发现：FlushAggregated 从未被任何调度器调用，非 critical 通知成死信（永滞 Redis）。
// Tier 2 加了 flush ticker + PendingTargets 扫描。本测试验证：
//   - warning（非 critical）升级通知进入 Redis 聚合队列（pending_notify key 存在）
//   - flusher 周期驱动后队列被清空（非死信，最终送出）
//
// 覆盖审计指出的 e2e 最大盲点之一：原 e2e 只用 critical 告警（旁路聚合），从不验证聚合链路。
package e2e_test

import (
	"context"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// warningPayload 构造 warning 级别告警（critical 旁路聚合，warning 才进聚合队列）。
func warningPayload(serviceSlug, fingerprint string) []byte {
	return []byte(`{
		"alerts": [{
			"status": "firing",
			"labels": {
				"alertname": "TestWarn",
				"severity": "warning",
				"instance": "test:8080",
				"service": "` + serviceSlug + `"
			},
			"annotations": {"summary": "测试警告"},
			"fingerprint": "` + fingerprint + `"
		}]
	}`)
}

var _ = Describe("通知聚合闭环（C3）", func() {
	It("warning 升级通知进聚合队列，flusher 周期后清空（非死信）", func() {
		ctx := context.Background()

		By("造团队/服务 + 一个值班用户（作为升级 target，使聚合按 user:<id> 分桶）")
		t := testEnv.seedTeam("聚合测试")
		s := testEnv.seedService("agg-api", t.ID)
		oncall, _ := testEnv.db().User.Create().
			SetUsername("agg-oncall").
			SetName("agg-oncall").
			SetEmail("agg-oncall@e2e.test").
			SetPhone("13800000001").
			Save(ctx)

		By("配置升级策略：delay=0，target=user（指向值班用户），channel=webhook")
		policy := testEnv.seedEscalationPolicy(adminToken, "agg-policy", []escLevel{{
			DelayMinutes: 0,
			Targets:      []escTarget{{Type: "user", TargetID: itoa(oncall.ID)}},
			Channels:     []string{"webhook"},
		}})
		testEnv.bindPolicyToService(s.ID, policy.ID)

		By("发 warning 告警（非 critical，应进聚合队列而非立即发送）")
		_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)
		resp := testEnv.sendWebhook(integToken, warningPayload(s.Slug, "fp-agg-warn-1"))
		Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
		_ = resp.Body.Close()
		incs := testEnv.waitForIncidentCount(1)

		By("升级触发（delay=0，level 推进到 1，触发通知）")
		testEnv.waitForEscalationLevel(incs[0].ID, 1)

		By("验证 Redis 出现 pending_notify 聚合队列（key 存在 = 通知被聚合捕获，非静默丢弃）")
		// 聚合按 target 分桶：user:<id>。给一点时间让 escalation worker 投递通知到队列。
		Eventually(func() int {
			keys, err := testEnv.Store.Redis.Keys(ctx, "vigil:pending_notify:*").Result()
			if err != nil {
				return 0
			}
			return len(keys)
		}, 10*time.Second, 500*time.Millisecond).
			Should(BeNumerically(">", 0),
				"warning 升级通知应进入 Redis 聚合队列（pending_notify key 存在）。若 0 说明聚合链路断裂或被当作 critical 旁路了")

		By("验证 flusher 周期驱动后队列被清空（非死信——这是 C3 修复的核心）")
		// flusher 间隔 = window(30s)/2 = 15s；窗口到点（30s）后 flusher 下次扫描会清空。
		// 总等待 ~45s（窗口 + 一次 flush 扫描）。这是 C3 修复的端到端验证：修复前 key 会
		// 永远存在（死信），修复后 flusher 清空它。
		Eventually(func() int {
			keys, _ := testEnv.Store.Redis.Keys(ctx, "vigil:pending_notify:*").Result()
			return len(keys)
		}, 60*time.Second, 1*time.Second).
			Should(Equal(0),
				"聚合队列应在 flusher 周期驱动后被清空（C3 修复：非死信，最终送出）。若仍存在说明 flusher 未接线")
	})
})
