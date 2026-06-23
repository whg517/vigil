//go:build integration

package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("鉴权三轨", func() {
	Describe("API Key 鉴权链", func() {
		It("登录拿 JWT → 创建 API Key → 用 X-Vigil-Key 访问受保护 API", func() {
			By("用 JWT 创建 API Key（返回一次性明文 token）")
			req := testEnv.authedJSON(http.MethodPost, adminToken, "/api-keys",
				map[string]any{"name": "e2e-key", "scope": []string{"incident.read"}})
			var keyResp struct {
				Token string `json:"token"`
			}
			doJSON(req, &keyResp)
			Expect(keyResp.Token).NotTo(BeEmpty(), "api-key 应返回一次性 token")

			By("用 X-Vigil-Key 头访问 /incidents（应通过身份解析）")
			listReq, _ := http.NewRequest(http.MethodGet, testEnv.apiURL("/incidents"), nil)
			listReq.Header.Set("X-Vigil-Key", keyResp.Token)
			listResp := doReq(listReq)
			defer func() { _ = listResp.Body.Close() }()
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("JWT 拒绝无效 token", func() {
		It("AUTH_ENABLED=true 时无效 JWT 应被拒", func() {
			req, _ := http.NewRequest(http.MethodGet, testEnv.apiURL("/incidents"), nil)
			req.Header.Set("Authorization", "Bearer invalid.jwt.token")
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK), "无效 token 应被拒")
		})
	})

	Describe("refresh token", func() {
		It("refresh token 能换新 access token", func() {
			By("登录拿 access + refresh")
			body, _ := json.Marshal(map[string]string{"username": "admin", "password": "changeme"})
			req, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/auth/login"), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			var tokens struct {
				Access  string `json:"access_token"`
				Refresh string `json:"refresh_token"`
			}
			doJSON(req, &tokens)
			Expect(tokens.Refresh).NotTo(BeEmpty(), "应返回 refresh token")

			By("用 refresh 换新 access")
			refreshBody, _ := json.Marshal(map[string]string{"refresh_token": tokens.Refresh})
			req2, _ := http.NewRequest(http.MethodPost, testEnv.apiURL("/auth/refresh"), bytes.NewReader(refreshBody))
			req2.Header.Set("Content-Type", "application/json")
			var newTokens struct {
				Access string `json:"access_token"`
			}
			doJSON(req2, &newTokens)
			Expect(newTokens.Access).NotTo(BeEmpty(), "refresh 应返回新 access token")
		})
	})
})
