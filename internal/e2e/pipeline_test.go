//go:build integration

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/kevin/vigil/ent/incident"
)

// TestPipeline_AlertCreatesIncident 验证完整告警闭环：
// webhook 接入 → 归一化（队列）→ 分诊建 incident → Event 与 Incident 分离落库。
//
// 这是 Vigil 的核心价值链，单元测试（sqlite mock）无法覆盖队列时序，
// 此处用真实 Asynq worker + 轮询验证端到端。
func TestPipeline_AlertCreatesIncident(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	// 1. 准备接入链路：团队 → 服务 → 接入点（拿 webhook token）
	team := env.SeedTeam(t, "支付")
	svc := env.SeedService(t, "pay-api", team.ID)
	_, integToken := env.SeedIntegration(t, token, "prometheus", team.ID, svc.ID)

	// 2. 发送 Prometheus Alertmanager 格式告警
	// 关键：labels["service"] 必须等于 Service.slug，triage 路据此匹配到 Service（见 route）。
	payload := []byte(`{
		"alerts": [{
			"status": "firing",
			"labels": {
				"alertname": "HighErrorRate",
				"severity": "critical",
				"instance": "pay-api:8080",
				"service": "` + svc.Slug + `"
			},
			"annotations": {"summary": "5xx 错误率超阈值"},
			"fingerprint": "fp-e2e-pipeline-1"
		}]
	}`)
	resp := env.SendWebhook(t, integToken, payload)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook: got %d, want 202", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. 轮询等待流水线建出 incident（ingestion→normalize→triage）
	incs := env.WaitForIncidentCount(t, 1)
	inc := incs[0]

	// 4. 断言 incident 字段：状态 triggered、severity critical、标题来自告警
	if inc.Status != incident.StatusTriggered {
		t.Errorf("incident status: got %s, want triggered", inc.Status)
	}
	if inc.Severity != incident.SeverityCritical {
		t.Errorf("incident severity: got %s, want critical", inc.Severity)
	}
	if inc.Title == "" {
		t.Errorf("incident title: empty, want derived from alert")
	}
	if inc.Number == "" {
		t.Errorf("incident number: empty, want INC-xxxx")
	}

	// 5. 断言 Event 与 Incident 分离（设计基线第 2 条）：Event 应落库
	eventCount, err := env.DB().Event.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount == 0 {
		t.Errorf("events: got 0, want ≥1 (Event/Incident separation)")
	}
}
