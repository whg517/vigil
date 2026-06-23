//go:build integration

package e2e

import (
	"net/http"
	"testing"

	"github.com/kevin/vigil/ent/incident"
)

// TestIncidentAction_AckResolveReopen 验证 Incident 操作（Web/IM 共用入口）的状态机：
// 登录拿 JWT → 触发告警建 incident → ack → resolve → reopen，每步断言状态。
func TestIncidentAction_AckResolveReopen(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	team := env.SeedTeam(t, "支付")
	svc := env.SeedService(t, "pay-api", team.ID)
	_, integToken := env.SeedIntegration(t, token, "prometheus", team.ID, svc.ID)

	// 触发告警，等待 incident 建出
	whResp := env.SendWebhook(t, integToken, promPayload(svc.Slug, "fp-action-1"))
	_ = whResp.Body.Close()
	incs := env.WaitForIncidentCount(t, 1)
	incID := incs[0].ID

	// ack：状态 triggered → acked，assignee 应来自 JWT（非 body）
	ackReq := env.AuthedJSON(t, http.MethodPost, token, "/incidents/"+itoa(incID)+"/ack", nil)
	ackResp, err := http.DefaultClient.Do(ackReq)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	defer func() { _ = ackResp.Body.Close() }()
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("ack: got %d, want 200", ackResp.StatusCode)
	}
	acked := env.WaitForIncidentStatus(t, incID, incident.StatusAcked)
	if acked == nil {
		t.Fatalf("ack: incident not acked")
	}

	// resolve：acked → resolved
	resReq := env.AuthedJSON(t, http.MethodPost, token, "/incidents/"+itoa(incID)+"/resolve", nil)
	resResp, err := http.DefaultClient.Do(resReq)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	defer func() { _ = resResp.Body.Close() }()
	if resResp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: got %d, want 200", resResp.StatusCode)
	}
	env.WaitForIncidentStatus(t, incID, incident.StatusResolved)

	// reopen：resolved → triggered（重新打开）
	reopenReq := env.AuthedJSON(t, http.MethodPost, token, "/incidents/"+itoa(incID)+"/reopen", nil)
	reopenResp, err := http.DefaultClient.Do(reopenReq)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopenResp.Body.Close() }()
	if reopenResp.StatusCode != http.StatusOK {
		t.Fatalf("reopen: got %d, want 200", reopenResp.StatusCode)
	}
	env.WaitForIncidentStatus(t, incID, incident.StatusTriggered)
}

// TestIncidentAction_NoTokenRejected 验证强制鉴权：AUTH_ENABLED=true 时无 token 应被拒。
func TestIncidentAction_NoTokenRejected(t *testing.T) {
	env := Setup(t)
	token := env.Login(t)
	t.Cleanup(func() { env.ResetDB(t) })

	team := env.SeedTeam(t, "团队")
	svc := env.SeedService(t, "svc", team.ID)
	_, integToken := env.SeedIntegration(t, token, "prometheus", team.ID, svc.ID)

	authResp := env.SendWebhook(t, integToken, promPayload(svc.Slug, "fp-auth-1"))
	_ = authResp.Body.Close()
	incs := env.WaitForIncidentCount(t, 1)
	incID := incs[0].ID

	// 无 Authorization 头访问 ack —— 应 401
	req, _ := http.NewRequest(http.MethodPost, env.APIURL("/incidents/"+itoa(incID)+"/ack"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ack without token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ack without token: got %d, want 401", resp.StatusCode)
	}
}

// TestIncidentAction_ListRequiresAuth 验证 list 也受鉴权保护。
func TestIncidentAction_ListRequiresAuth(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	// 无 token 列 incident —— 应 401
	req, _ := http.NewRequest(http.MethodGet, env.APIURL("/incidents"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list without token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("list without token: got %d, want 401", resp.StatusCode)
	}

	// 有 token 应 200
	token := env.Login(t)
	listReq := env.AuthedJSON(t, http.MethodGet, token, "/incidents", nil)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list with token: %v", err)
	}
	defer func() { _ = listResp.Body.Close() }()
	if listResp.StatusCode != http.StatusOK {
		t.Errorf("list with token: got %d, want 200", listResp.StatusCode)
	}
}

// promPayload 构造 Prometheus Alertmanager 格式告警（labels.service = serviceSlug）。
func promPayload(serviceSlug, fingerprint string) []byte {
	return []byte(`{
		"alerts": [{
			"status": "firing",
			"labels": {
				"alertname": "TestAlert",
				"severity": "critical",
				"instance": "test:8080",
				"service": "` + serviceSlug + `"
			},
			"annotations": {"summary": "测试告警"},
			"fingerprint": "` + fingerprint + `"
		}]
	}`)
}
