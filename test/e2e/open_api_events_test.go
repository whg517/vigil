//go:build integration

// open_api_events_test.go 开放 API 投递 e2e（T5.1，能力域 01 §5 / 10 §A3）。
//
// 覆盖：admin 签发 API Key（继承 admin 角色 → 有 event.create）→ POST /api/v1/events
// 带 X-Vigil-Key + integration_id + 通用 JSON payload → 走与 webhook 同一分诊链路建 incident。
// 验证「外部系统凭 API Key 程序化投递告警」端到端可用（鉴权三轨中的 API Key 轨）。
package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/kevin/vigil/ent/incident"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("开放 API 投递（T5.1）", func() {
	Describe("X-Vigil-Key 程序化投递走分诊建 incident", func() {
		It("POST /api/v1/events（X-Vigil-Key + integration_id）→ 分诊建出 incident", func() {
			By("造团队/服务/接入点（webhook 型 → GenericJSON 适配器）")
			t := testEnv.seedTeam("开放接入")
			s := testEnv.seedService("open-api-svc", t.ID)
			// GenericJSONAdapter.Type()=="webhook"，故接入点 type 用 webhook。
			integ, _ := testEnv.seedIntegration(adminToken, "webhook", t.ID, s.ID)

			By("admin 签发 API Key（继承 admin 的 org_admin → 具 event.create）")
			apiKey := testEnv.createAPIKey(adminToken, "e2e-open-api")
			Expect(apiKey).To(HavePrefix("vgl_"), "API Key 明文应 vgl_ 前缀")

			By("POST /api/v1/events：X-Vigil-Key 鉴权 + 通用 JSON payload（labels.service 匹配 service slug）")
			payload := map[string]any{
				"integration_id":  integ.ID,
				"source_event_id": "open-api-evt-1",
				"severity":        "critical",
				"status":          "firing",
				"summary":         "开放 API 投递告警",
				"labels":          map[string]any{"service": s.Slug},
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/events"), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Vigil-Key", apiKey)
			resp := doReq(req)
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted), "开放 API 投递应返回 202")
			_ = resp.Body.Close()

			By("轮询等待流水线建出 incident（ingest→normalize→triage，与 webhook 同链路）")
			incs := testEnv.waitForIncidentCount(1)
			Expect(incs[0].Status).To(Equal(incident.StatusTriggered), "应建出 triggered incident")
			Expect(incs[0].Severity).To(Equal(incident.SeverityCritical), "severity 应来自 payload=critical")
		})

		It("无 X-Vigil-Key 投递应被拒（未鉴权 401）", func() {
			By("造接入点")
			t := testEnv.seedTeam("开放接入2")
			s := testEnv.seedService("open-api-svc2", t.ID)
			integ, _ := testEnv.seedIntegration(adminToken, "webhook", t.ID, s.ID)

			By("不带任何鉴权头 POST /api/v1/events → 应 401（v1 组要求身份）")
			payload := map[string]any{"integration_id": integ.ID, "labels": map[string]any{"service": s.Slug}}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/events"), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp := doReq(req)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized),
				"开放 API 无鉴权应 401（走 v1 RBAC 链路）")
		})
	})
})
