//go:build integration

// change_password_test.go 强制改密 e2e（QA 审计 C8）。
//
// 审计发现：默认 admin/changeme 无强制改密机制，可长期使用。修复加了 must_change_password
// 标志 + forcePasswordGuard：标志为 true 时仅放行 /auth/change-password，其余业务 API 返 403。
// 本测试独立造一个 must_change_password=true 的用户验证闭环（不影响 admin，admin 在
// reseedAdmin 里已清标志供其它用例使用）。
package e2e_test

import (
	"context"
	"net/http"

	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/auth"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("强制改密闭环（C8）", func() {
	It("must_change_password=true 时业务 API 被 403 拦截，改密后放行", func() {
		ctx := context.Background()

		By("造一个 must_change_password=true 的用户（模拟刚 seed 的默认 admin）")
		u, err := testEnv.db().User.Create().
			SetUsername("forcepw").
			SetName("ForcePW").
			SetEmail("forcepw@e2e.test").
			SetPasswordHash(auth.HashPassword("oldpass123")).
			SetMustChangePassword(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred(), "create forcepw user")
		tok := loginAs(testEnv, "forcepw", "oldpass123")
		Expect(tok).NotTo(BeEmpty(), "forcepw 应能登录拿 token（登录本身不受改密守卫拦）")

		By("该用户访问业务 API（GET /incidents）→ 应 403 + must_change_password 提示")
		req := testEnv.authedJSON(http.MethodGet, tok, "/incidents", nil)
		resp := doReq(req)
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"must_change_password=true 时业务 API 应被 forcePasswordGuard 拦截返 403")
		// body 应含引导改密的提示
		bodyBytes := make([]byte, 200)
		n, _ := resp.Body.Read(bodyBytes)
		Expect(string(bodyBytes[:n])).To(ContainSubstring("must_change_password"),
			"403 响应应提示 must_change_password 引导前端走改密流程")

		By("改密（POST /auth/change-password）→ 应 200")
		changeReq := testEnv.authedJSON(http.MethodPost, tok, "/auth/change-password",
			map[string]string{"old_password": "oldpass123", "new_password": "newpass-456"})
		changeResp := doReq(changeReq)
		defer func() { _ = changeResp.Body.Close() }()
		Expect(changeResp.StatusCode).To(Equal(http.StatusOK), "改密请求应成功（旧密码正确+新密码达标）")

		By("改密后 must_change_password 标志应被清除")
		updated, err := testEnv.db().User.Get(ctx, u.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.MustChangePassword).To(BeFalse(), "改密后标志应清除")

		By("改密后业务 API → 应 200（守卫放行）")
		// 重新登录拿新 token（旧 token 仍有效，但模拟新会话更贴近真实）
		newTok := loginAs(testEnv, "forcepw", "newpass-456")
		Expect(newTok).NotTo(BeEmpty(), "用新密码应能登录")
		req2 := testEnv.authedJSON(http.MethodGet, newTok, "/incidents", nil)
		resp2 := doReq(req2)
		defer func() { _ = resp2.Body.Close() }()
		Expect(resp2.StatusCode).To(Equal(http.StatusOK),
			"改密后业务 API 应放行（标志已清，forcePasswordGuard 不再拦截）")
	})

	It("改密：旧密码错误应拒绝（防绕过改密）", func() {
		ctx := context.Background()
		_, err := testEnv.db().User.Create().
			SetUsername("forcepw2").
			SetEmail("forcepw2@e2e.test").
			SetPasswordHash(auth.HashPassword("correct-old-1")).
			SetMustChangePassword(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred())
		tok := loginAs(testEnv, "forcepw2", "correct-old-1")

		By("用错误旧密码改密 → 应 401")
		req := testEnv.authedJSON(http.MethodPost, tok, "/auth/change-password",
			map[string]string{"old_password": "WRONG-old-pass", "new_password": "newpass-789"})
		resp := doReq(req)
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized), "旧密码错误应拒绝改密")

		// 标志不应被清除（改密失败）
		u, _ := testEnv.db().User.Query().Where(user.UsernameEQ("forcepw2")).Only(ctx)
		Expect(u.MustChangePassword).To(BeTrue(), "改密失败标志应保留")
	})

	It("改密：弱新密码应拒绝（防改成弱密码绕过安全）", func() {
		ctx := context.Background()
		_, err := testEnv.db().User.Create().
			SetUsername("forcepw3").
			SetEmail("forcepw3@e2e.test").
			SetPasswordHash(auth.HashPassword("strong-old-1")).
			SetMustChangePassword(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred())
		tok := loginAs(testEnv, "forcepw3", "strong-old-1")

		By("用弱新密码改密 → 应 400")
		req := testEnv.authedJSON(http.MethodPost, tok, "/auth/change-password",
			map[string]string{"old_password": "strong-old-1", "new_password": "weak"})
		resp := doReq(req)
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "弱新密码应拒绝（强度校验）")
	})
})
