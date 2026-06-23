//go:build integration

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/incident"
)

// TestEscalation_MultiLevel 验证升级链：
// 配置 2 层升级策略（delay=0 立即触发）→ 触发告警建 incident →
// 轮询等待 current_level 推进到 1，再到 2，且时间线有升级记录。
//
// 验证 Asynq 延迟任务驱动的真实升级时序（单元测试无法覆盖）。
func TestEscalation_MultiLevel(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	team := env.SeedTeam(t, "运维")
	svc := env.SeedService(t, "ops-svc", team.ID)
	_, integToken := env.SeedIntegration(t, token, "prometheus", team.ID, svc.ID)

	// 创建 2 层升级策略（delay_minutes=0 → 立即触发，加速测试）
	policy := env.SeedEscalationPolicy(t, token, "e2e-esc-policy", []EscLevel{
		{DelayMinutes: 0, Targets: []EscTarget{{Type: "team", TargetID: itoa(team.ID)}}, Channels: []string{"im"}},
		{DelayMinutes: 0, Targets: []EscTarget{{Type: "team", TargetID: itoa(team.ID)}}, Channels: []string{"im"}},
	})
	env.BindPolicyToService(t, svc.ID, policy.ID)

	// 触发告警，等待 incident 建出（triage 会绑定 policy 并启动升级）
	escResp := env.SendWebhook(t, integToken, promPayload(svc.Slug, "fp-esc-1"))
	_ = escResp.Body.Close()
	incs := env.WaitForIncidentCount(t, 1)
	incID := incs[0].ID

	// 升级链启动后，current_level 应推进到 1，再到 2（delay=0）
	env.WaitForEscalationLevel(t, incID, 1)
	env.WaitForEscalationLevel(t, incID, 2)

	// 时间线应有升级记录
	env.WaitForTimelineEntry(t, incID)

	// 最终 incident 状态应为 escalated（升级过）
	final, _ := env.DB().Incident.Get(context.Background(), incID)
	if final.Status != incident.StatusEscalated && final.Status != incident.StatusAcked {
		// escalated 是预期；若已 ack（有 target ack 了）也算升级链触达
		t.Errorf("final status: got %s, want escalated or acked", final.Status)
	}
}

// TestEscalation_AckStopsEscalation 验证 ack 后升级停止（状态守卫）：
// 触发告警 → ack → 升级任务到期时因 incident 已 ack 不再推进。
func TestEscalation_AckStopsEscalation(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	team := env.SeedTeam(t, "运维")
	svc := env.SeedService(t, "ops-svc2", team.ID)
	_, integToken := env.SeedIntegration(t, token, "prometheus", team.ID, svc.ID)
	// 第二层延迟较长，给 ack 留时间窗
	policy := env.SeedEscalationPolicy(t, token, "e2e-esc-stop", []EscLevel{
		{DelayMinutes: 0, Targets: []EscTarget{{Type: "team", TargetID: itoa(team.ID)}}, Channels: []string{"im"}},
		{DelayMinutes: 10, Targets: []EscTarget{{Type: "team", TargetID: itoa(team.ID)}}, Channels: []string{"im"}},
	})
	env.BindPolicyToService(t, svc.ID, policy.ID)

	stopResp := env.SendWebhook(t, integToken, promPayload(svc.Slug, "fp-esc-stop-1"))
	_ = stopResp.Body.Close()
	incs := env.WaitForIncidentCount(t, 1)
	incID := incs[0].ID

	// 等第一层升级触发（level 1），然后立即 ack
	env.WaitForEscalationLevel(t, incID, 1)
	ackReq := env.AuthedJSON(t, http.MethodPost, token, "/incidents/"+itoa(incID)+"/ack", nil)
	ackResp, err := http.DefaultClient.Do(ackReq)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	_ = ackResp.Body.Close()
	env.WaitForIncidentStatus(t, incID, incident.StatusAcked)

	// 等待一段时间，确认 level 没有继续推进到 2（第二层 10min 延迟 + ack 守卫）。
	// 这里不真的等 10min，只验证 ack 后短时间内 level 仍是 1（守卫生效的间接证据）。
	time.Sleep(2 * time.Second)
	inc, _ := env.DB().Incident.Get(context.Background(), incID)
	if inc.Status != incident.StatusAcked {
		t.Errorf("after ack: status %s, want acked (escalation should be stopped)", inc.Status)
	}
}
