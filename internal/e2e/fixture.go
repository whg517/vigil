//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
)

// allTables 是全部业务表名，供 ResetDB 做 TRUNCATE。
// 列表与 ent/migrate/schema.go 的 schema.Table.Name 一一对应（含关联表）。
// 必须包含所有表，否则 ResetDB 会因外键残留失败。
var allTables = []string{
	"action_items",
	"ai_insights",
	"api_keys",
	"audit_logs",
	"escalation_policies",
	"escalation_policy_schedules",
	"events",
	"im_account_bindings",
	"incident_actions",
	"incident_responders",
	"incidents",
	"integrations",
	"notification_rules",
	"notification_templates",
	"postmortems",
	"raw_events",
	"role_bindings",
	"roles",
	"rotation_participants",
	"rotations",
	"runbooks",
	"schedules",
	"service_runbooks",
	"service_schedules",
	"services",
	"suppression_rules",
	"team_role_bindings",
	"team_users",
	"teams",
	"timeline_items",
	"users",
}

// ResetDB 清空所有业务表，保证每个测试的数据独立。
// 使用 RESTART IDENTITY 重置自增序列，CASCADE 连带清理外键依赖。
// schema_migrations 表保留（迁移版本记录，测试不重跑迁移）。
func (e *Env) ResetDB(t *testing.T) {
	t.Helper()
	// 动态拼出 TRUNCATE 表列表：t1, t2, ... RESTART IDENTITY CASCADE
	stmt := "TRUNCATE "
	for i, tbl := range allTables {
		if i > 0 {
			stmt += ", "
		}
		stmt += tbl
	}
	stmt += " RESTART IDENTITY CASCADE"
	if _, err := e.App.Store.SQL.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("reset db: %v", err)
	}
}

// SeedTeam 创建一个团队并返回，slug 唯一避免测试间冲突。
func (e *Env) SeedTeam(t *testing.T, name string) *ent.Team {
	t.Helper()
	team, err := e.DB().Team.Create().
		SetName(name).
		SetSlug(slugify(name)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed team %q: %v", name, err)
	}
	return team
}

// SeedUser 创建一个用户（不带角色绑定，测试按需授权）。
func (e *Env) SeedUser(t *testing.T, username string) *ent.User {
	t.Helper()
	u, err := e.DB().User.Create().
		SetUsername(username).
		SetEmail(username + "@e2e.test").
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	return u
}

// SeedService 创建一个服务并绑定团队，auto_create_incident=true 保证告警直接建 incident。
func (e *Env) SeedService(t *testing.T, name string, teamID int) *ent.Service {
	t.Helper()
	svc, err := e.DB().Service.Create().
		SetName(name).
		SetSlug(slugify(name)).
		SetTeamID(teamID).
		SetAutoCreateIncident(true).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed service %q: %v", name, err)
	}
	return svc
}

// SeedIntegration 创建一个接入点并返回，含 webhook 鉴权 token（测试发告警用）。
// 通过 HTTP API 创建（而非直接插库），以验证真实 create 链路并拿到一次性 token。
func (e *Env) SeedIntegration(t *testing.T, token, kind string, teamID, serviceID int) (*ent.Integration, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":       "e2e-integration-" + kind,
		"type":       kind, // prometheus | grafana | ...
		"config":     map[string]any{},
		"team_id":    teamID,
		"service_id": serviceID,
	})
	req, _ := http.NewRequest(http.MethodPost, e.APIURL("/integrations"), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var got struct {
		*ent.Integration
		Token string `json:"token"`
	}
	doReqJSON(t, req, &got)
	if got.Integration == nil {
		t.Fatalf("seed integration: nil in response")
	}
	return got.Integration, got.Token
}

// EscTarget 描述升级通知目标（对应 schema.Target）。
type EscTarget struct {
	Type     string `json:"type"`      // schedule | user | team
	TargetID string `json:"target_id"` // schedule_id / user_id / team_id
}

// EscLevel 描述升级层级，测试用短 delay（分钟级）加速。
type EscLevel struct {
	DelayMinutes int         `json:"delay_minutes"`
	Targets      []EscTarget `json:"targets"`
	Channels     []string    `json:"notify_channels"`
}

// SeedEscalationPolicy 通过 HTTP API 创建升级策略并返回。
// levels 用 JSON 透明传递（handler 解析为 schema.EscalationLevel）。
func (e *Env) SeedEscalationPolicy(t *testing.T, token, name string, levels []EscLevel) *ent.EscalationPolicy {
	t.Helper()
	// API 期望 level 字段从 1 开始递增。
	rawLevels := make([]map[string]any, len(levels))
	for i, lv := range levels {
		rawLevels[i] = map[string]any{
			"level":           i + 1,
			"delay_minutes":   lv.DelayMinutes,
			"targets":         lv.Targets,
			"notify_channels": lv.Channels,
		}
	}
	body, _ := json.Marshal(map[string]any{
		"name":   name,
		"levels": rawLevels,
	})
	req, _ := http.NewRequest(http.MethodPost, e.APIURL("/escalation-policies"), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var got ent.EscalationPolicy
	doReqJSON(t, req, &got)
	return &got
}

// BindPolicyToService 把升级策略绑定到 service（triage 据此启动升级链）。
func (e *Env) BindPolicyToService(t *testing.T, serviceID, policyID int) {
	t.Helper()
	if err := e.DB().Service.UpdateOneID(serviceID).
		SetEscalationPolicyID(policyID).
		Exec(context.Background()); err != nil {
		t.Fatalf("bind policy to service: %v", err)
	}
}

// SendWebhook 向接入点发送告警 payload（token 鉴权），返回响应。
// 触发 ingestion → 归一化 → 分诊 流水线。
func (e *Env) SendWebhook(t *testing.T, token string, payload []byte) *http.Response {
	t.Helper()
	// webhook 路由在 public 组：POST /api/v1/webhook/:token（自带 token 鉴权，不走 JWT）
	req, _ := http.NewRequest(http.MethodPost, e.APIURL("/webhook/"+token), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send webhook: %v", err)
	}
	return resp
}

// Login 用 admin/changeme 登录，返回 JWT access token。
// 依赖 Bootstrap 内 SeedDefaultAdmin 创建的默认管理员。
func (e *Env) Login(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "changeme",
	})
	req, _ := http.NewRequest(http.MethodPost, e.APIURL("/auth/login"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var got struct {
		AccessToken string `json:"access_token"`
	}
	doReqJSON(t, req, &got)
	if got.AccessToken == "" {
		t.Fatalf("login: empty access token")
	}
	return got.AccessToken
}

// AuthedJSON 构造一个带 JWT + JSON body 的请求。
func (e *Env) AuthedJSON(t *testing.T, method, token, path string, body any) *http.Request {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.APIURL(path), bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// doReqJSON 执行请求、校验 2xx、解码响应体到 v，并确保 body 被关闭。
// 合并为单函数以便 bodyclose 静态分析能识别到关闭点。
func doReqJSON(t *testing.T, req *http.Request, v any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s: got status %d, want 2xx", req.Method, req.URL, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// slugify 把名字转成合法 slug（小写 + 短横线 + 纳秒时间戳后缀保证唯一）。
func slugify(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}
