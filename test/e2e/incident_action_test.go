//go:build integration

package e2e_test

import (
	"net/http"

	"github.com/kevin/vigil/ent/incident"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Incident 操作", func() {
	Describe("ack/resolve/reopen 状态机", func() {
		It("triggered → ack → resolved → triggered（reopen）", func() {
			By("触发告警建 incident")
			t := testEnv.seedTeam("支付")
			s := testEnv.seedService("pay-api", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			resp := testEnv.sendWebhook(integToken, promPayload(s.Slug, "fp-action-1"))
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
			_ = resp.Body.Close()

			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			By("ack：triggered → acked，assignee 来自 JWT（非 body）")
			ackReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/ack", nil)
			ackResp := doReq(ackReq)
			defer func() { _ = ackResp.Body.Close() }()
			Expect(ackResp.StatusCode).To(Equal(http.StatusOK))
			testEnv.waitForIncidentStatus(incID, incident.StatusAcked)

			By("resolve：acked → resolved")
			resReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/resolve", nil)
			resResp := doReq(resReq)
			defer func() { _ = resResp.Body.Close() }()
			Expect(resResp.StatusCode).To(Equal(http.StatusOK))
			testEnv.waitForIncidentStatus(incID, incident.StatusResolved)

			By("reopen：resolved → triggered")
			reopenReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/reopen", nil)
			reopenResp := doReq(reopenReq)
			defer func() { _ = reopenResp.Body.Close() }()
			Expect(reopenResp.StatusCode).To(Equal(http.StatusOK))
			testEnv.waitForIncidentStatus(incID, incident.StatusTriggered)
		})
	})

	Describe("RBAC 鉴权", func() {
		It("AUTH_ENABLED=true 时无 token 应被拒（401）", func() {
			By("触发告警建 incident")
			t := testEnv.seedTeam("团队")
			s := testEnv.seedService("svc", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			resp := testEnv.sendWebhook(integToken, promPayload(s.Slug, "fp-auth-1"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			By("无 Authorization 头访问 ack —— 应 401")
			req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/incidents/"+itoa(incID)+"/ack"), nil)
			resp2, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp2.Body.Close() }()
			Expect(resp2.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("list 受鉴权保护：无 token 401，有 token 200", func() {
			By("无 token 列 incident —— 应 401")
			req, _ := http.NewRequest(http.MethodGet, testEnv.apiURL("/incidents"), nil)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

			By("有 token 应 200")
			listReq := testEnv.authedJSON(http.MethodGet, adminToken, "/incidents", nil)
			listResp := doReq(listReq)
			defer func() { _ = listResp.Body.Close() }()
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		})
	})
})
