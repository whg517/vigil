//go:build integration

// incident_merge_test.go 人工合并 e2e（N1.1，能力域 02 §2.6 / 审计 M7）。
//
// 覆盖单元测试触及不到的端到端契约：两张真实 incident（各由独立告警流水线建出）→
// HTTP POST /incidents/:id/merge → 源单收口（merged_into/closed）、events/responders 转移到主单、
// 双写时间线。验证「多张单收敛成一张」的核心降噪动作在真实 HTTP + PG 下正确。
package e2e_test

import (
	"context"
	"net/http"

	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Incident 合并（N1.1）", func() {
	Describe("把源单合并进主单", func() {
		It("POST /incidents/:id/merge → 源单 merged_into 指向主单且 closed，events 转移，双写时间线", func() {
			By("造两张独立 incident：两个不同 service（不同 slug 分属不同聚合组，保证建两张单）")
			t := testEnv.seedTeam("支付")
			svcA := testEnv.seedService("pay-api-a", t.ID)
			svcB := testEnv.seedService("pay-api-b", t.ID)
			_, integA := testEnv.seedIntegration(adminToken, "prometheus", t.ID, svcA.ID)
			_, integB := testEnv.seedIntegration(adminToken, "prometheus", t.ID, svcB.ID)

			respA := testEnv.sendWebhook(integA, promPayload(svcA.Slug, "fp-merge-a"))
			_ = respA.Body.Close()
			respB := testEnv.sendWebhook(integB, promPayload(svcB.Slug, "fp-merge-b"))
			_ = respB.Body.Close()

			By("等两张单都建出（按 ID 升序：incs[0]=主单，incs[1]=源单）")
			incs := testEnv.waitForIncidentCount(2)
			target := incs[0]
			source := incs[1]

			By("记录源单关联的 Event（合并后应转移到主单）")
			ctx := context.Background()
			srcEventIDs, err := testEnv.db().Incident.QueryEvents(source).IDs(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(srcEventIDs).NotTo(BeEmpty(), "源单应至少有 1 条关联 Event")

			By("POST /incidents/:target/merge，body.source_incident_ids=[source]")
			body := map[string]any{"source_incident_ids": []int{source.ID}}
			req := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(target.ID)+"/merge", body)
			resp := doReq(req)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "合并应返回 200")

			By("源单终态：merged_into 指向主单 + status=closed")
			// 合并是同步写：无需轮询，直接回读断言（避免引入不必要的时序）。
			srcFresh, err := testEnv.db().Incident.Get(ctx, source.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(srcFresh.Status).To(Equal(incident.StatusClosed), "源单应收口为 closed")
			Expect(srcFresh.MergedInto).To(Equal(itoa(target.ID)), "源单 merged_into 应指向主单 ID")

			By("主单仍活跃（未被合并动作改状态）")
			tgtFresh, err := testEnv.db().Incident.Get(ctx, target.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(tgtFresh.Status).NotTo(Equal(incident.StatusClosed), "主单不应被合并动作关闭")

			By("events 转移：源单原关联的 Event 现挂到主单")
			for _, eid := range srcEventIDs {
				evt, gerr := testEnv.db().Event.Get(ctx, eid)
				Expect(gerr).NotTo(HaveOccurred())
				incID, qerr := evt.QueryIncident().OnlyID(ctx)
				Expect(qerr).NotTo(HaveOccurred(), "转移后 Event 应仍归属某单")
				Expect(incID).To(Equal(target.ID), "源单的 Event 应转移到主单")
			}

			By("双写时间线：主单记 merged（target 角色），源单记 merged（source 角色）")
			tgtMerged, err := testEnv.db().TimelineItem.Query().
				Where(
					timelineitem.HasIncidentWith(incident.IDEQ(target.ID)),
					timelineitem.TypeEQ(timelineitem.TypeMerged),
				).Count(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tgtMerged).To(BeNumerically(">", 0), "主单应有 merged 时间线")

			srcMerged, err := testEnv.db().TimelineItem.Query().
				Where(
					timelineitem.HasIncidentWith(incident.IDEQ(source.ID)),
					timelineitem.TypeEQ(timelineitem.TypeMerged),
				).Count(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(srcMerged).To(BeNumerically(">", 0), "源单应有 merged 时间线")
		})

		It("合并进自己应被拒（400 failed_precondition）", func() {
			By("造一张 incident")
			t := testEnv.seedTeam("运维")
			s := testEnv.seedService("ops-merge-self", t.ID)
			_, integ := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)
			resp := testEnv.sendWebhook(integ, promPayload(s.Slug, "fp-merge-self"))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			id := incs[0].ID

			By("把单合并进自己 → 应 400（ErrMergeIntoSelf）")
			body := map[string]any{"source_incident_ids": []int{id}}
			req := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(id)+"/merge", body)
			mergeResp := doReq(req)
			defer func() { _ = mergeResp.Body.Close() }()
			Expect(mergeResp.StatusCode).To(Equal(http.StatusBadRequest),
				"合并进自己应被拒 400")
		})
	})
})
