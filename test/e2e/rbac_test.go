//go:build integration

// rbac_test.go RBAC 越权防护 e2e（QA 审计 C1）。
//
// 审计发现：RouteGuard 中间件定义了却从未生效（因 c.Path() 含 group 前缀导致查表
// 永不命中），所有写路由对任意登录用户敞开。本测试验证修复后授权分级真正生效：
//   - subscriber（只读角色）访问写路由应被拒 403
//   - admin（org_admin 全权）访问同一写路由应通过 200
//
// 这是审计指出的核心盲点：原 e2e 全用 admin 跑，从未用受限角色验证权限边界。
package e2e_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RBAC 越权防护（C1）", func() {
	Describe("受限角色访问写路由应被拒", func() {
		It("subscriber（只读）POST /incidents/:id/escalate 应 403，admin 应 200", func() {
			By("造 incident（admin 触发告警建单）")
			t := testEnv.seedTeam("支付")
			s := testEnv.seedService("pay-api", t.ID)
			_, integToken := testEnv.seedIntegration(adminToken, "prometheus", t.ID, s.ID)
			resp := testEnv.sendWebhook(integToken, promPayload(s.Slug, "fp-rbac-1"))
			Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
			_ = resp.Body.Close()
			incs := testEnv.waitForIncidentCount(1)
			incID := incs[0].ID

			By("创建 subscriber（只读角色）用户")
			_, viewerToken := testEnv.seedUserWithRole("viewer1", "subscriber")
			Expect(viewerToken).NotTo(BeEmpty(), "subscriber 应能登录拿 token")

			By("subscriber POST /incidents/:id/escalate → 应 403（无 incident.escalate 权限）")
			viewerReq := testEnv.authedJSON(http.MethodPost, viewerToken, "/incidents/"+itoa(incID)+"/escalate", nil)
			viewerResp := doReq(viewerReq)
			defer func() { _ = viewerResp.Body.Close() }()
			Expect(viewerResp.StatusCode).To(Equal(http.StatusForbidden),
				"subscriber 无 escalate 权限，RouteGuard 应拒 403（修复前因守卫失效返 200）")

			By("admin POST /incidents/:id/escalate → 应 200（org_admin 全权）")
			adminReq := testEnv.authedJSON(http.MethodPost, adminToken, "/incidents/"+itoa(incID)+"/escalate", nil)
			adminResp := doReq(adminReq)
			defer func() { _ = adminResp.Body.Close() }()
			Expect(adminResp.StatusCode).To(Equal(http.StatusOK), "admin 有 escalate 权限")
		})

		It("subscriber POST /role-bindings 应 403（无 role.assign，防自授 org_admin 提权）", func() {
			By("创建 subscriber 用户")
			_, viewerToken := testEnv.seedUserWithRole("viewer2", "subscriber")

			By("subscriber 尝试创建 role-binding（试图给自己提权）→ 应 403")
			body := map[string]any{"user_id": 1, "role_id": 1}
			req := testEnv.authedJSON(http.MethodPost, viewerToken, "/role-bindings", body)
			resp := doReq(req)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
				"subscriber 无 role.assign 权限，不能创建角色绑定（防越权提权到 org_admin）")
		})

		It("subscriber POST /api-keys 应 403（无 admin.apikey.manage）", func() {
			By("创建 subscriber 用户")
			_, viewerToken := testEnv.seedUserWithRole("viewer3", "subscriber")

			By("subscriber 尝试签发 API Key → 应 403")
			body := map[string]any{"name": "malicious-key", "scope": []string{"incident.read"}}
			req := testEnv.authedJSON(http.MethodPost, viewerToken, "/api-keys", body)
			resp := doReq(req)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
				"subscriber 无 admin.apikey.manage，不能签发 API Key（审计 C1 防持久化后门）")
		})

		It("subscriber 可访问读路由（GET /incidents → 200，权限不被过度收紧）", func() {
			By("创建 subscriber 用户")
			_, viewerToken := testEnv.seedUserWithRole("viewer4", "subscriber")

			By("subscriber GET /incidents → 应 200（subscriber 有 incident.view）")
			req := testEnv.authedJSON(http.MethodGet, viewerToken, "/incidents", nil)
			resp := doReq(req)
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"subscriber 有 incident.view，读路由应放行（确认守卫不过度收紧只读权限）")
		})
	})
})
