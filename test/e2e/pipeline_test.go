//go:build integration

package e2e_test

import (
	"context"
	"net/http"

	"github.com/kevin/vigil/ent/incident"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("告警流水线", func() {
	Describe("完整告警闭环", func() {
		It("webhook 接入 → 归一化 → 分诊建 incident，且 Event/Incident 分离落库", func() {
			// 这是 Vigil 的核心价值链，单元测试（sqlite mock）无法覆盖队列时序。
			By("准备接入链路：团队 → 服务 → 接入点（拿 webhook token）")
			t := testEnv.seedTeam("支付")
			s := testEnv.seedService("pay-api", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)

			By("发送 Prometheus Alertmanager 格式告警")
			// labels["service"] 必须等于 Service.slug，triage 路据此匹配到 Service。
			payload := []byte(`{
				"alerts": [{
					"status": "firing",
					"labels": {
						"alertname": "HighErrorRate",
						"severity": "critical",
						"instance": "pay-api:8080",
						"service": "` + s.Slug + `"
					},
					"annotations": {"summary": "5xx 错误率超阈值"},
					"fingerprint": "fp-e2e-pipeline-1"
				}]
			}`)
			resp := testEnv.sendWebhook(integToken, payload)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted), "webhook 应返回 202")

			By("轮询等待流水线建出 incident（ingestion→normalize→triage）")
			incs := testEnv.waitForIncidentCount(1)
			inc := incs[0]

			By("断言 incident 字段：状态/严重度/标题/编号")
			Expect(inc.Status).To(Equal(incident.StatusTriggered), "状态应为 triggered")
			Expect(inc.Severity).To(Equal(incident.SeverityCritical), "严重度应为 critical")
			Expect(inc.Title).NotTo(BeEmpty(), "标题应来自告警")
			Expect(inc.Number).NotTo(BeEmpty(), "应有 incident 编号")

			By("断言 Event 与 Incident 分离（设计基线第 2 条）：Event 应落库")
			eventCount, err := testEnv.db().Event.Query().Count(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(eventCount).To(BeNumerically(">", 0), "Event 应落库")
		})
	})
})
