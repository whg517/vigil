//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kevin/vigil/ent"
	entservice "github.com/kevin/vigil/ent/service"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// 方案C 端到端：未路由告警（带 service/team label）经真实 webhook→异步流水线
// 自动供给 source=auto 服务并建单，随后验证治理链路（source 筛选、转正、策略 team_id 过滤）。
// 依赖 suite 开启 VIGIL_TRIAGE_AUTO_PROVISION_ENABLED（见 suite_test.go）。
var _ = ginkgo.Describe("Service 自动供给与治理 (方案C)", func() {
	// createPolicyForTeam 经 API 建一个归属指定团队的升级策略，返回其 id。
	createPolicyForTeam := func(name string, teamID int) int {
		levels := []map[string]any{{
			"level": 1, "delay_minutes": 0,
			"targets": []map[string]any{}, "notify_channels": []string{"im"},
		}}
		body, _ := json.Marshal(map[string]any{"name": name, "team_id": teamID, "levels": levels})
		req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/escalation-policies"), bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		var got ent.EscalationPolicy
		doJSON(req, &got)
		gomega.Expect(got.ID).To(gomega.BeNumerically(">", 0), "策略创建应返回 id")
		return got.ID
	}

	// setTeamDefaultPolicy 经 PATCH /teams/:id 设团队默认升级策略（特性C 后端），回读校验。
	setTeamDefaultPolicy := func(teamID, polID int) {
		req := testEnv.authedJSON(http.MethodPatch, adminToken, "/teams/"+itoa(teamID),
			map[string]any{"default_escalation_policy_id": polID})
		var got struct {
			DefaultEscalationPolicyID *int `json:"default_escalation_policy_id"`
		}
		doJSON(req, &got)
		gomega.Expect(got.DefaultEscalationPolicyID).NotTo(gomega.BeNil(), "响应应回带默认策略 id")
		gomega.Expect(*got.DefaultEscalationPolicyID).To(gomega.Equal(polID), "默认策略应被设置")
	}

	// integrationNoDefaultService 建一个不绑默认 service 的接入点（保证告警走未路由→自动供给），返回 token。
	integrationNoDefaultService := func(teamID int) string {
		body, _ := json.Marshal(map[string]any{
			"name": "ap-integ", "type": "prometheus", "config": map[string]any{}, "team_id": teamID,
		})
		req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/integrations"), bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		var got struct {
			Token string `json:"token"`
		}
		doJSON(req, &got)
		gomega.Expect(got.Token).NotTo(gomega.BeEmpty(), "接入点应返回 webhook token")
		return got.Token
	}

	// apPayload 构造带 service + team label 的 critical 告警（未匹配任何既有服务）。
	apPayload := func(serviceSlug, teamSlug string) []byte {
		return []byte(`{"alerts":[{"status":"firing","labels":{` +
			`"alertname":"AutoProvisionAlert","severity":"critical",` +
			`"service":"` + serviceSlug + `","team":"` + teamSlug + `"},` +
			`"annotations":{"summary":"自动供给测试告警"},"fingerprint":"ap-fp-1"}]}`)
	}

	// waitForServiceBySlug 轮询直到出现指定 slug 的服务。
	waitForServiceBySlug := func(slug string) *ent.Service {
		var svc *ent.Service
		gomega.Eventually(func() error {
			s, err := testEnv.db().Service.Query().
				Where(entservice.SlugEQ(slug)).WithTeam().Only(context.Background())
			if err != nil {
				return err
			}
			svc = s
			return nil
		}, 15*time.Second, 200*time.Millisecond).Should(gomega.Succeed(),
			"等待自动供给出服务 "+slug)
		return svc
	}

	ginkgo.It("未路由告警自动建 source=auto 服务+建单，并支持筛选/转正/策略 team_id 过滤", func() {
		// —— 前置：团队 + 归属该团队的升级策略 + 设为团队默认策略 ——
		team := testEnv.seedTeam("apteam")
		polID := createPolicyForTeam("ap-pol", team.ID)
		setTeamDefaultPolicy(team.ID, polID)

		// —— 接入点（无默认 service）——
		tok := integrationNoDefaultService(team.ID)

		// —— 发未路由告警（service=ap-svc 无既有服务；team=<slug> 可解析；critical）——
		resp := testEnv.sendWebhook(tok, apPayload("ap-svc", team.Slug))
		_ = resp.Body.Close()
		gomega.Expect(resp.StatusCode).To(gomega.BeNumerically("<", 300), "webhook 应被接受")

		// —— 断言 1：自动供给出 source=auto 服务，归属正确团队，继承团队默认策略 ——
		svc := waitForServiceBySlug("ap-svc")
		gomega.Expect(svc.Source).To(gomega.Equal(entservice.SourceAuto), "source 应为 auto")
		gomega.Expect(svc.ProvisionedAt).NotTo(gomega.BeNil(), "provisioned_at 应有值")
		gomega.Expect(svc.Edges.Team).NotTo(gomega.BeNil())
		gomega.Expect(svc.Edges.Team.ID).To(gomega.Equal(team.ID), "服务应归属解析出的团队")
		boundPol, err := svc.QueryEscalationPolicy().Only(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(boundPol.ID).To(gomega.Equal(polID), "应继承团队默认升级策略")

		// —— 断言 2：建出 incident（critical 自动建单），归属自动供给的服务 ——
		incs := testEnv.waitForIncidentCount(1)
		svcOfInc, err := incs[0].QueryService().Only(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(svcOfInc.ID).To(gomega.Equal(svc.ID), "incident 应归属自动供给的服务")

		// —— 断言 3：治理 —— GET /services?source=auto 含该服务 ——
		req := testEnv.authedJSON(http.MethodGet, adminToken, "/services?source=auto", nil)
		var autoList []struct {
			Slug   string `json:"slug"`
			Source string `json:"source"`
		}
		doJSON(req, &autoList)
		found := false
		for _, s := range autoList {
			if s.Slug == "ap-svc" {
				gomega.Expect(s.Source).To(gomega.Equal("auto"))
				found = true
			}
		}
		gomega.Expect(found).To(gomega.BeTrue(), "source=auto 列表应含 ap-svc")

		// —— 断言 4：转正 —— PATCH source=manual → 库中变 manual ——
		req = testEnv.authedJSON(http.MethodPatch, adminToken, "/services/"+itoa(svc.ID),
			map[string]any{"source": "manual"})
		resp2 := doReq(req)
		_ = resp2.Body.Close()
		gomega.Expect(resp2.StatusCode).To(gomega.Equal(http.StatusOK), "转正应 200")
		adopted, err := testEnv.db().Service.Get(context.Background(), svc.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(adopted.Source).To(gomega.Equal(entservice.SourceManual), "转正后 source=manual")

		// —— 断言 5：策略 team_id 过滤（特性C 后端）返回该团队的策略 ——
		req = testEnv.authedJSON(http.MethodGet, adminToken, "/escalation-policies?team_id="+itoa(team.ID), nil)
		var pols []struct {
			ID int `json:"id"`
		}
		doJSON(req, &pols)
		gomega.Expect(pols).To(gomega.HaveLen(1), "team_id 过滤应只返回该团队的 1 条策略")
		gomega.Expect(pols[0].ID).To(gomega.Equal(polID))
	})
})
