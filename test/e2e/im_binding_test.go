//go:build integration

// im_binding_test.go IM 账号绑定 e2e（QA 审计 C6）。
//
// 审计发现：Mapper.BindAccount 全仓零调用方，用户无法绑定 IM → ResolveUser 永远
// ErrNotBound → 所有 IM 操作 403。Tier 2 补了 POST /users/:id/im-accounts 端点。
// 本测试验证端到端闭环：绑定 → ResolveUser 命中 → IM 回调路径可解析到 User。
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/kevin/vigil/ent/imaccountbinding"
	"github.com/kevin/vigil/internal/im"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("IM 账号绑定闭环（C6）", func() {
	It("POST /users/:id/im-accounts 绑定后 ResolveUser 能命中", func() {
		ctx := context.Background()

		By("创建一个普通用户（供绑定 IM 账号）")
		u, _ := testEnv.db().User.Create().
			SetUsername("imbinder").
			SetName("imbinder").
			SetEmail("imbinder@e2e.test").
			Save(ctx)

		By("admin 调 POST /users/:id/im-accounts 绑定飞书账号")
		bindReq := testEnv.authedJSON(http.MethodPost, adminToken, "/users/"+itoa(u.ID)+"/im-accounts",
			map[string]string{"platform": "feishu", "account_id": "ou_e2e_feishu_001"})
		bindResp := doReq(bindReq)
		defer func() { _ = bindResp.Body.Close() }()
		Expect(bindResp.StatusCode).To(Equal(http.StatusCreated), "绑定请求应成功")

		By("验证 im_account_bindings 表有记录")
		cnt, err := testEnv.db().IMAccountBinding.Query().
			Where(imaccountbinding.PlatformEQ(imaccountbinding.PlatformFeishu), imaccountbinding.AccountIDEQ("ou_e2e_feishu_001")).
			Count(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(cnt).To(Equal(1), "独立表应有一条绑定记录")

		By("用 im.Mapper.ResolveUser 验证 IM 回调路径能解析到该用户（M8.6 核心契约）")
		mapper := im.NewMapper(testEnv.db())
		resolved, err := mapper.ResolveUser(ctx, "feishu", "ou_e2e_feishu_001")
		Expect(err).NotTo(HaveOccurred(), "已绑定账号 ResolveUser 应命中（修复前 ErrNotBound → IM 操作全 403）")
		Expect(resolved.ID).To(Equal(u.ID), "ResolveUser 应返回绑定的用户")
	})

	It("GET /users/:id/im-accounts 列出已绑定账号", func() {
		ctx := context.Background()
		u, _ := testEnv.db().User.Create().
			SetUsername("imbinder2").
			SetEmail("imbinder2@e2e.test").
			Save(ctx)

		By("绑定两个平台")
		for _, acc := range []struct{ platform, id string }{
			{"feishu", "ou_list_test"},
			{"dingtalk", "dt_list_test"},
		} {
			req := testEnv.authedJSON(http.MethodPost, adminToken, "/users/"+itoa(u.ID)+"/im-accounts",
				map[string]string{"platform": acc.platform, "account_id": acc.id})
			resp := doReq(req)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			resp.Body.Close()
		}

		By("GET 列表应返回 2 条")
		listReq := testEnv.authedJSON(http.MethodGet, adminToken, "/users/"+itoa(u.ID)+"/im-accounts", nil)
		listResp := doReq(listReq)
		defer func() { _ = listResp.Body.Close() }()
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		var accs []struct {
			Platform  string `json:"platform"`
			AccountID string `json:"account_id"`
		}
		Expect(json.NewDecoder(listResp.Body).Decode(&accs)).To(Succeed())
		Expect(accs).To(HaveLen(2), "应列出 2 个已绑定账号")
	})

	It("未绑定的账号 ResolveUser 应返回 ErrNotBound", func() {
		ctx := context.Background()
		mapper := im.NewMapper(testEnv.db())
		_, err := mapper.ResolveUser(ctx, "feishu", "ou_not_bound_xyz")
		Expect(err).To(Equal(im.ErrNotBound), "未绑定账号应返回 ErrNotBound（IM 回调据此提示去绑定）")
	})
})
