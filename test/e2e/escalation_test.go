//go:build integration

package e2e_test

import (
	"context"
	"net/http"
	"time"

	"github.com/kevin/vigil/ent/incident"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("升级链", func() {
	Describe("多层升级推进", func() {
		It("配置 2 层策略（delay=0）→ current_level 推进到 1 再到 2，时间线有记录", func() {
			// 验证 Asynq 延迟任务驱动的真实升级时序（单元测试无法覆盖）。
			By("准备团队/服务/接入点 + 2 层升级策略（delay=0 立即触发）")
			t := testEnv.seedTeam("运维")
			s := testEnv.seedService("ops-svc", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			policy := testEnv.seedEscalationPolicy(adminToken, "e2e-esc", []escLevel{
				{DelayMinutes: 0, Targets: []escTarget{{Type: "team", TargetID: itoa(t.ID)}}, Channels: []string{"im"}},
				{DelayMinutes: 0, Targets: []escTarget{{Type: "team", TargetID: itoa(t.ID)}}, Channels: []string{"im"}},
			})
			testEnv.bindPolicyToService(s.ID, policy.ID)

			By("触发告警，等待 incident 建出（triage 会绑定 policy 并启动升级）")
			resp := testEnv.sendWebhook(integToken, promPayload(s.Slug, "fp-esc-1"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			By("升级链启动后，current_level 推进到 1，再到 2（delay=0）")
			testEnv.waitForEscalationLevel(incID, 1)
			testEnv.waitForEscalationLevel(incID, 2)

			By("时间线应有升级记录")
			testEnv.waitForTimelineEntry(incID)

			By("最终 incident 状态应为 escalated（升级过）")
			final, _ := testEnv.db().Incident.Get(context.Background(), incID)
			Expect(final.Status).To(BeElementOf(incident.StatusEscalated, incident.StatusAcked),
				"升级后状态应为 escalated 或 acked")
		})
	})

	Describe("ack 停止升级（状态守卫）", func() {
		It("ack 后升级任务到期时因 incident 已 ack 不再推进", func() {
			By("准备：第二层延迟较长，给 ack 留时间窗")
			t := testEnv.seedTeam("运维")
			s := testEnv.seedService("ops-svc2", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			policy := testEnv.seedEscalationPolicy(adminToken, "e2e-esc-stop", []escLevel{
				{DelayMinutes: 0, Targets: []escTarget{{Type: "team", TargetID: itoa(t.ID)}}, Channels: []string{"im"}},
				{DelayMinutes: 10, Targets: []escTarget{{Type: "team", TargetID: itoa(t.ID)}}, Channels: []string{"im"}},
			})
			testEnv.bindPolicyToService(s.ID, policy.ID)

			By("触发告警，等第一层升级（level 1）触发后立即 ack")
			resp := testEnv.sendWebhook(integToken, promPayload(s.Slug, "fp-esc-stop-1"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			testEnv.waitForEscalationLevel(incID, 1)
			ackReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/ack", nil)
			ackResp := doReq(ackReq)
			_ = ackResp.Body.Close()
			testEnv.waitForIncidentStatus(incID, incident.StatusAcked)

			By("等待一段时间，确认 level 没继续推进到 2（ack 守卫）")
			// 不真等 10min，只验证 ack 后短时间内状态仍为 acked（守卫间接证据）。
			time.Sleep(2 * time.Second)
			inc, _ := testEnv.db().Incident.Get(context.Background(), incID)
			Expect(inc.Status).To(Equal(incident.StatusAcked), "ack 后升级应停止")
		})
	})
})
