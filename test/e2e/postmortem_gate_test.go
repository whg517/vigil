//go:build integration

// postmortem_gate_test.go 复盘闸门 + closed 终态 e2e（T1.2/T4.1，能力域 04）。
//
// 覆盖：critical incident resolve 后直接 close 被复盘闸门拦截（400）→ skip-postmortem 放行 →
// close 成功进入 closed 终态。验证「critical 须先复盘或显式跳过才能关闭」的治理约束在真实
// HTTP + PG 下生效，且 skip 后能正常收口。
package e2e_test

import (
	"context"
	"net/http"

	"github.com/kevin/vigil/ent/incident"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("复盘闸门 + closed 终态（T1.2/T4.1）", func() {
	Describe("critical 事件复盘闸门", func() {
		It("resolved 后直接 close 被拒（400）→ skip-postmortem → close 成功（closed）", func() {
			By("造 critical incident（critical severity 告警）")
			t := testEnv.seedTeam("支付")
			s := testEnv.seedService("pay-crit", t.ID)
			_, integ := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)
			// promPayload 固定 severity=critical，建出的单即 critical。
			resp := testEnv.sendWebhook(integ, promPayload(s.Slug, "fp-pm-gate"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID
			Expect(incs[0].Severity).To(Equal(incident.SeverityCritical), "应为 critical 单")

			By("推进到 resolved（ack → resolve）")
			ackResp := doReq(testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/ack", nil))
			_ = ackResp.Body.Close()
			testEnv.waitForIncidentStatus(incID, incident.StatusAcked)
			resResp := doReq(testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/resolve", nil))
			_ = resResp.Body.Close()
			testEnv.waitForIncidentStatus(incID, incident.StatusResolved)

			By("直接 close → 应被复盘闸门拒 400（critical 未复盘/未跳过）")
			closeReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/close", nil)
			closeResp := doReq(closeReq)
			Expect(closeResp.StatusCode).To(Equal(http.StatusBadRequest),
				"critical 未复盘不可 close（T4.1 闸门）")
			_ = closeResp.Body.Close()

			By("确认闸门拦截后单据仍停在 resolved（未被误关）")
			stillResolved, err := testEnv.db().Incident.Get(context.Background(), incID)
			Expect(err).NotTo(HaveOccurred())
			Expect(stillResolved.Status).To(Equal(incident.StatusResolved),
				"闸门拦截后单据应停在 resolved，不进 closed")

			By("skip-postmortem 显式跳过复盘闸门")
			skipReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/skip-postmortem", nil)
			skipResp := doReq(skipReq)
			Expect(skipResp.StatusCode).To(Equal(http.StatusOK), "skip-postmortem 应 200")
			_ = skipResp.Body.Close()

			By("再次 close → 应成功进入 closed 终态")
			close2 := doReq(testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/close", nil))
			Expect(close2.StatusCode).To(Equal(http.StatusOK), "跳过复盘后 close 应成功")
			_ = close2.Body.Close()
			testEnv.waitForIncidentStatus(incID, incident.StatusClosed)

			By("closed 是终态：再 close 幂等 200（不报错）")
			close3 := doReq(testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/close", nil))
			Expect(close3.StatusCode).To(Equal(http.StatusOK), "重复 close 应幂等 200")
			_ = close3.Body.Close()
		})
	})
})
