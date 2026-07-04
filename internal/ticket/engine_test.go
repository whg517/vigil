package ticket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// setupPM 建 team + incident + postmortem + 1 个未建单 ActionItem，返回 client / pmID / actionItemID / teamID。
func setupPM(t *testing.T) (*ent.Client, int, int, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:ticket_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	tm := c.Team.Create().SetName("ops").SetSlug("ops-" + t.Name()).SaveX(ctx)
	inc := c.Incident.Create().SetNumber("INC-" + t.Name()).SetTitle("t").
		SetSeverity("critical").SetStatus("resolved").SetTeamID(tm.ID).SaveX(ctx)
	pm := c.Postmortem.Create().SetIncidentID(inc.ID).SetStatus("published").
		SetGeneratedBy("human").SetSections(map[string]any{}).SaveX(ctx)
	ai := c.ActionItem.Create().SetDescription("补监控").SetPostmortemID(pm.ID).SaveX(ctx)
	return c, pm.ID, ai.ID, tm.ID
}

// TestEngine_OnPublished_CreatesAndBackfills 配置工单集成后，发布联动为 ActionItem 建单并回写 tracker_url。
func TestEngine_OnPublished_CreatesAndBackfills(t *testing.T) {
	c, pmID, aiID, teamID := setupPM(t)
	ctx := context.Background()

	var receivedCredential string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCredential = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"tracker_url":"https://tk/T-9","external_id":"T-9"}`))
	}))
	defer srv.Close()

	// team 级工单集成，带凭据。
	c.TicketIntegration.Create().SetName("jira").SetType("webhook").
		SetEndpoint(srv.URL).SetCredential("cred-xyz").SetTeamID(teamID).SetEnabled(true).SaveX(ctx)

	eng := NewEngine(c, NewWebhookAdapter(true))
	eng.OnPostmortemPublished(ctx, pmID)

	got := c.ActionItem.GetX(ctx, aiID)
	if got.TrackerURL != "https://tk/T-9" {
		t.Fatalf("tracker_url not backfilled: got %q", got.TrackerURL)
	}
	if receivedCredential != "Bearer cred-xyz" { //nolint:gosec // 测试字面量,非真实凭据
		t.Errorf("credential not sent as bearer: got %q", receivedCredential)
	}
}

// TestEngine_OnPublished_OrgLevelFallback 无 team 级集成时兜底 org 级（无 team 归属）集成。
func TestEngine_OnPublished_OrgLevelFallback(t *testing.T) {
	c, pmID, aiID, _ := setupPM(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tracker_url":"https://tk/ORG-1"}`))
	}))
	defer srv.Close()
	// org 级集成（不设 team）。
	c.TicketIntegration.Create().SetName("org-webhook").SetType("webhook").
		SetEndpoint(srv.URL).SetEnabled(true).SaveX(ctx)

	NewEngine(c, NewWebhookAdapter(true)).OnPostmortemPublished(ctx, pmID)
	if got := c.ActionItem.GetX(ctx, aiID); got.TrackerURL != "https://tk/ORG-1" {
		t.Fatalf("org-level fallback did not backfill: got %q", got.TrackerURL)
	}
}

// TestEngine_OnPublished_Unreachable_DoesNotBlock 工单系统不可达时不 panic、不回写、不报错（best-effort）。
func TestEngine_OnPublished_Unreachable_DoesNotBlock(t *testing.T) {
	c, pmID, aiID, teamID := setupPM(t)
	ctx := context.Background()
	// 指向已关闭端口。
	c.TicketIntegration.Create().SetName("down").SetType("webhook").
		SetEndpoint("http://127.0.0.1:1").SetTeamID(teamID).SetEnabled(true).SaveX(ctx)

	// 不应 panic；tracker_url 保持空（建单失败不回写）。
	NewEngine(c, NewWebhookAdapter(true)).OnPostmortemPublished(ctx, pmID)
	if got := c.ActionItem.GetX(ctx, aiID); got.TrackerURL != "" {
		t.Errorf("unreachable should not backfill tracker_url, got %q", got.TrackerURL)
	}
}

// TestEngine_OnPublished_NoIntegration_NoOp 未配集成时空操作（不建单不报错）。
func TestEngine_OnPublished_NoIntegration_NoOp(t *testing.T) {
	c, pmID, aiID, _ := setupPM(t)
	ctx := context.Background()
	NewEngine(c, NewWebhookAdapter(true)).OnPostmortemPublished(ctx, pmID)
	if got := c.ActionItem.GetX(ctx, aiID); got.TrackerURL != "" {
		t.Errorf("no integration should leave tracker_url empty, got %q", got.TrackerURL)
	}
}

// TestEngine_OnPublished_SkipsAlreadyTracked 已有 tracker_url 的 ActionItem 不重复建单。
func TestEngine_OnPublished_SkipsAlreadyTracked(t *testing.T) {
	c, pmID, aiID, teamID := setupPM(t)
	ctx := context.Background()
	// 预置已建单。
	c.ActionItem.UpdateOneID(aiID).SetTrackerURL("https://existing/T-0").ExecX(ctx)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c.TicketIntegration.Create().SetName("wh").SetType("webhook").
		SetEndpoint(srv.URL).SetTeamID(teamID).SetEnabled(true).SaveX(ctx)

	NewEngine(c, NewWebhookAdapter(true)).OnPostmortemPublished(ctx, pmID)
	if called {
		t.Error("should not create ticket for already-tracked action item")
	}
	if got := c.ActionItem.GetX(ctx, aiID); got.TrackerURL != "https://existing/T-0" {
		t.Errorf("existing tracker_url overwritten: got %q", got.TrackerURL)
	}
}

// TestEngine_NotImplementedAdapter_DoesNotBlock Jira/禅道预留适配器建单降级不回写不阻断。
func TestEngine_NotImplementedAdapter_DoesNotBlock(t *testing.T) {
	c, pmID, aiID, teamID := setupPM(t)
	ctx := context.Background()
	c.TicketIntegration.Create().SetName("jira").SetType("jira").
		SetEndpoint("https://jira.example.com").SetTeamID(teamID).SetEnabled(true).SaveX(ctx)

	// 只注册 jira 预留适配器。
	NewEngine(c, NewJiraAdapter()).OnPostmortemPublished(ctx, pmID)
	if got := c.ActionItem.GetX(ctx, aiID); got.TrackerURL != "" {
		t.Errorf("not-implemented adapter should not backfill, got %q", got.TrackerURL)
	}
}

// TestCredentialNotPlaintext 凭据经 Sensitive 字段存储：JSON API 响应不含明文、String() 脱敏。
//
// 安全保证（handler list/get 直接 c.JSON 该 ent 实体）：
//   - Credential 字段有 `json:"-"` tag（Sensitive 生成）→ JSON 序列化恒不含凭据，API 不回显。
//   - String()（日志/调试打印）对 Sensitive 字段输出 <sensitive>，凭据不进日志。
//
// 注：DB 读回的实体内存态仍持有明文凭据（Engine 据此传给适配器建单），这是设计需要——
// 凭据只在进程内传递，不经 API/日志外泄。
func TestCredentialNotPlaintext(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:ticket_cred?mode=memory&cache=shared&_fk=1")
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	ti := c.TicketIntegration.Create().SetName("wh").SetType("webhook").
		SetEndpoint("https://x").SetCredential("super-secret").SetEnabled(true).SaveX(ctx)

	// String()（日志打印）对 Sensitive 字段脱敏——凭据不出现在打印输出。
	if str := ti.String(); strings.Contains(str, "super-secret") {
		t.Errorf("credential leaked in String(): %s", str)
	}
	// JSON 序列化（API 响应经此）恒不含凭据（json:"-"）。
	reread := c.TicketIntegration.GetX(ctx, ti.ID)
	raw, err := json.Marshal(reread)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Errorf("credential leaked in JSON response: %s", raw)
	}
	if strings.Contains(string(raw), "credential") {
		t.Errorf("credential field present in JSON (should be json:\"-\"): %s", raw)
	}
}
