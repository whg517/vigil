//go:build integration

package e2e_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("健康检查", func() {
	Describe("/health 端点", func() {
		It("依赖（PG+Redis）都通时返回 200", func() {
			resp, err := http.Get(testEnv.baseURL() + "/health")
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})

		It("暴露 version 字段且 checks 标记 up", func() {
			resp, err := http.Get(testEnv.baseURL() + "/health")
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()

			var got struct {
				Status  string            `json:"status"`
				Version string            `json:"version"`
				Checks  map[string]string `json:"checks"`
			}
			Expect(json.NewDecoder(resp.Body).Decode(&got)).To(Succeed())
			Expect(got.Version).NotTo(BeEmpty(), "应暴露 version")
			Expect(got.Checks["postgres"]).To(Equal("up"), "postgres 应 up")
			Expect(got.Checks["redis"]).To(Equal("up"), "redis 应 up")
		})
	})
})
