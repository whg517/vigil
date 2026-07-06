//go:build integration

// notification_delivery_test.go 通知送达记录 e2e（T2.2 / B22 / M13，能力域 07）。
//
// 覆盖：critical 告警触发升级 → notifier 逐 target 走降级链 → 每次尝试落一条 Notification
// 送达记录（sent/failed/suppressed）→ GET /incidents/:id/notifications 可查。验证「通知发给了谁、
// 走哪个通道、送达三态」的送达账本在真实 HTTP + PG 下落库且可查询。
//
// 稳定性：用 critical severity（跳过聚合，立即走降级链，避免等聚合窗口的时序）+ team 型 target
// （解算到已绑定的活跃成员，保证有真实收件人）。无真实通道注册时降级链记 failed——无论 sent/failed，
// 都会落一条 Notification 记录，故断言「至少 1 条记录」稳定不 flaky。
package e2e_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("通知送达记录（T2.2）", func() {
	Describe("升级触发后落通知送达记录", func() {
		It("critical 告警 → 升级通知 team 成员 → Notification 落库且 GET /notifications 可查", func() {
			By("造团队 + 活跃成员（team 型升级 target 需解算到真实收件人）")
			t := testEnv.seedTeam("值守")
			member := testEnv.seedActiveUser("notif-member")
			testEnv.bindUserToTeam(member.ID, t.ID)
			s := testEnv.seedService("notif-svc", t.ID)
			_, integ := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			By("配 1 层升级策略（delay=0 立即触发），target=team，channel=webhook")
			policy := testEnv.seedEscalationPolicy(adminToken, "e2e-notif", []escLevel{
				{DelayMinutes: 0, Targets: []escTarget{{Type: "team", TargetID: itoa(t.ID)}}, Channels: []string{"webhook"}},
			})
			testEnv.bindPolicyToService(s.ID, policy.ID)

			By("触发 critical 告警 → 建单 → 升级链启动 → 通知 team 成员")
			resp := testEnv.sendWebhook(integ, promPayload(s.Slug, "fp-notif-1"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			By("轮询等待通知送达记录落库（升级 → notifier 落库为异步链路）")
			recs := testEnv.waitForNotificationRecords(incID, 1)
			Expect(recs).NotTo(BeEmpty(), "应落至少 1 条通知送达记录")
			// 送达记录关联到该成员（team target 展开为逐成员 NotifyTarget）。
			var hitMember bool
			for _, r := range recs {
				if r.UserID == member.ID {
					hitMember = true
				}
				Expect(string(r.Status)).To(BeElementOf("sent", "failed", "suppressed", "pending"),
					"送达状态应为三态之一")
			}
			Expect(hitMember).To(BeTrue(), "送达记录应关联到被通知的 team 成员")

			By("GET /incidents/:id/notifications 端点应返回这些记录")
			listReq := testEnv.authedJSON(http.MethodGet, adminToken, "/incidents/"+itoa(incID)+"/notifications", nil)
			listResp := doReq(listReq)
			defer func() { _ = listResp.Body.Close() }()
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			var page struct {
				Items []struct {
					ID      int    `json:"id"`
					Channel string `json:"channel"`
					Status  string `json:"status"`
				} `json:"items"`
				Total int `json:"total"`
			}
			Expect(json.NewDecoder(listResp.Body).Decode(&page)).To(Succeed(), "解码通知列表")
			Expect(page.Total).To(BeNumerically(">=", 1), "端点应返回送达记录总数 >= 1")
			Expect(page.Items).NotTo(BeEmpty(), "端点应返回送达记录条目")
		})
	})
})
