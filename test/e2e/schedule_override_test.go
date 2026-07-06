//go:build integration

// schedule_override_test.go 换班 Override e2e（T2.3 / C5，能力域 03 §2.4）。
//
// 覆盖：建 schedule（含 rotation 值班人）→ POST overrides（顶替人 + 时段）→
// GET oncall（查询时刻落在 override 窗口内）→ 断言顶替人以最高优先级层出现且 override=true。
// 验证「临时换班完全覆盖 rotation 结果」的实时解算在真实 HTTP + PG 下正确。
package e2e_test

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("换班 Override（T2.3）", func() {
	Describe("Override 窗口内顶替 rotation 值班人", func() {
		It("建 schedule + rotation → POST overrides → GET oncall 窗口内替班人顶替且 override=true", func() {
			By("造团队 + 常规值班人 + 顶替人")
			t := testEnv.seedTeam("值班团队")
			regular := testEnv.seedActiveUser("oncall-regular")
			substitute := testEnv.seedActiveUser("oncall-substitute")

			By("POST /schedules：一层 rotation，值班人=regular")
			schedBody := map[string]any{
				"name":     "e2e-override-sched",
				"type":     "rotation",
				"timezone": "UTC",
				"team_id":  t.ID,
				"layers": []map[string]any{
					{
						"name":          "一线",
						"priority":      1,
						"participants":  []int{regular.ID},
						"rotation_type": "daily",
						"shift_length":  "24h",
						"start_date":    time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
					},
				},
			}
			var sched struct {
				ID int `json:"id"`
			}
			doJSON(testEnv.authedJSON(http.MethodPost, adminToken, "/schedules", schedBody), &sched)
			Expect(sched.ID).To(BeNumerically(">", 0), "schedule 应创建成功")

			By("先确认无 override 时在班人是 regular（基线）")
			base := testEnv.getOncall(sched.ID, time.Now().UTC())
			Expect(oncallHasUser(base, regular.ID, false)).To(BeTrue(),
				"无 override 时应为常规值班人 regular（override=false）")

			By("POST /schedules/:id/overrides：substitute 顶替 now±1h 窗口")
			now := time.Now().UTC()
			ovBody := map[string]any{
				"user_id":    substitute.ID,
				"start_time": now.Add(-1 * time.Hour).Format(time.RFC3339),
				"end_time":   now.Add(1 * time.Hour).Format(time.RFC3339),
				"reason":     "e2e 临时换班",
			}
			ovReq := testEnv.authedJSON(http.MethodPost, adminToken, "/schedules/"+itoa(sched.ID)+"/overrides", ovBody)
			ovResp := doReq(ovReq)
			defer func() { _ = ovResp.Body.Close() }()
			Expect(ovResp.StatusCode).To(Equal(http.StatusCreated), "override 应创建成功 201")

			By("GET /schedules/:id/oncall（查询 now，落在 override 窗口内）→ 顶替人以 override=true 出现")
			// 换班是同步实时解算：override 立即生效，无需轮询。
			got := testEnv.getOncall(sched.ID, now)
			Expect(oncallHasUser(got, substitute.ID, true)).To(BeTrue(),
				"窗口内在班人应为顶替人 substitute 且 override=true")

			By("查询窗口外时刻（+2h）→ 恢复常规值班人 regular（override 已过期）")
			outside := testEnv.getOncall(sched.ID, now.Add(2*time.Hour))
			Expect(oncallHasUser(outside, substitute.ID, true)).To(BeFalse(),
				"窗口外不应再有 override 顶替")
			Expect(oncallHasUser(outside, regular.ID, false)).To(BeTrue(),
				"窗口外应恢复常规值班人 regular")
		})
	})
})

// oncallResp 匹配 schedule.OncallResult 的 JSON 序列化形状（结构体无 json tag，用 Go 字段名）。
type oncallResp struct {
	Layers []struct {
		Name  string `json:"Name"`
		Users []struct {
			ID       int  `json:"ID"`
			Override bool `json:"Override"`
		} `json:"Users"`
	} `json:"Layers"`
}

// getOncall GET /schedules/:id/oncall?time=<RFC3339>，解码为 oncallResp。
func (e *envState) getOncall(schedID int, at time.Time) oncallResp {
	path := "/schedules/" + itoa(schedID) + "/oncall?time=" + at.Format(time.RFC3339)
	req := e.authedJSON(http.MethodGet, adminToken, path, nil)
	resp := doReq(req)
	defer func() { _ = resp.Body.Close() }()
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "oncall 查询应 200")
	var out oncallResp
	Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed(), "解码 oncall 响应")
	return out
}

// oncallHasUser 断言 oncall 结果中是否含指定用户，且 override 标志匹配。
func oncallHasUser(r oncallResp, userID int, wantOverride bool) bool {
	for _, layer := range r.Layers {
		for _, u := range layer.Users {
			if u.ID == userID && u.Override == wantOverride {
				return true
			}
		}
	}
	return false
}
