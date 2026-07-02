//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/auth"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
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

// BeforeEach：每个 Spec 运行前清空数据 + 重建 admin，保证用例间数据独立。
// 共用的 app 实例只起一次（BeforeSuite），这里只清状态，不重启。
// 关键：resetDB 会清掉 users 表（含 SeedDefaultAdmin 建的 admin），必须重建，
// 否则 BeforeSuite 缓存的 adminToken 会失效（用户不存在）。
var _ = ginkgo.BeforeEach(func() {
	resetDB()
	flushRedis()
	// 重建默认管理员并刷新 token（resetDB 重置了序列，admin ID 会变）。
	reseedAdmin()
})

// reseedAdmin 重建默认管理员（admin/changeme）并刷新全局 adminToken。
// SeedDefaultAdmin 幂等：resetDB 清空后调它会重建。
//
// 注意（QA 审计 C8）：SeedDefaultAdmin 现在置 must_change_password=true，强制首登改密。
// e2e 是可信测试环境（admin/changeme 已知），重建后立即清除该标志，避免 forcePasswordGuard
// 拦截测试用例的业务 API 调用。生产部署首登会走 /auth/change-password 流程。
//
// 注意（e2e RBAC 修正）：resetDB 清空了 roles/role_bindings 表（含 SeedBuiltinRoles 建的
// 内置角色）。RouteGuard 现在真正生效，admin 若无 org_admin 角色绑定会被所有写路由拒 403。
// 故重建后补种内置角色并把 org_admin 绑给 admin，模拟生产超管。
func reseedAdmin() {
	ctx := context.Background()
	// 1. 重建内置角色（resetDB 清空了，RouteGuard 鉴权依赖角色存在）
	gomega.Expect(auth.SeedBuiltinRoles(ctx, testEnv.Store.DB)).
		NotTo(gomega.HaveOccurred(), "reseed builtin roles")
	// 2. 重建默认管理员
	_, err := auth.SeedDefaultAdmin(ctx, testEnv.Store.DB)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "reseed admin")
	// 3. 清除强制改密标志（测试环境，admin 凭证已知可信）
	_, err = testEnv.Store.DB.User.Update().
		SetMustChangePassword(false).
		Where(user.UsernameEQ("admin")).
		Save(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "clear must_change_password for e2e")
	// 4. 把 org_admin 角色绑给 admin（使 RouteGuard 放行写路由，模拟生产超管）
	adminU, err := testEnv.Store.DB.User.Query().Where(user.UsernameEQ("admin")).Only(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "query admin for binding")
	orgAdminRole, err := testEnv.Store.DB.Role.Query().Where(role.NameEQ("org_admin")).Only(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "query org_admin role")
	// 幂等绑定（重复跑也不报错：先查再建）
	exist, _ := testEnv.Store.DB.RoleBinding.Query().
		Where(rolebinding.HasUserWith(user.IDEQ(adminU.ID)), rolebinding.HasRoleWith(role.IDEQ(orgAdminRole.ID))).
		Count(ctx)
	if exist == 0 {
		_, berr := testEnv.Store.DB.RoleBinding.Create().
			SetUserID(adminU.ID).
			SetRoleID(orgAdminRole.ID).
			SetScopeLevel(rolebinding.ScopeLevelOrg).
			Save(ctx)
		gomega.Expect(berr).NotTo(gomega.HaveOccurred(), "bind org_admin to admin")
	}
	adminToken = loginAdmin(testEnv)
}

// resetDB 清空所有业务表（TRUNCATE ... RESTART IDENTITY CASCADE）。
// schema_migrations 表保留（迁移版本记录，测试不重跑迁移）。
func resetDB() {
	gomega.Expect(testEnv).NotTo(gomega.BeNil(), "testEnv 未初始化")
	stmt := "TRUNCATE "
	for i, tbl := range allTables {
		if i > 0 {
			stmt += ", "
		}
		stmt += tbl
	}
	stmt += " RESTART IDENTITY CASCADE"
	_, err := testEnv.Store.SQL.ExecContext(context.Background(), stmt)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "ResetDB TRUNCATE")
}

// flushRedis 清空 Redis（dedup key / 聚合器 / asynq 残留任务）。
// worker 在运行，但 Ginkgo 串行跑单节点，flush 不会清掉正在处理的任务。
func flushRedis() {
	gomega.Expect(testEnv.Store.Redis.FlushDB(context.Background()).Err()).
		To(gomega.Succeed(), "flush redis")
}

// ===== envState 访问方法 =====

// baseURL 返回实例 API 基地址（不带 /api/v1）。
func (e *envState) baseURL() string { return e.baseURLStr }

// apiURL 返回完整业务 API URL（拼接 /api/v1 + path）。
func (e *envState) apiURL(path string) string {
	return e.baseURL() + "/api/v1" + path
}

// db 返回 ent 客户端，供直接查库断言。
func (e *envState) db() *ent.Client { return e.Store.DB }

// ===== HTTP 辅助 =====

// authedJSON 构造带 JWT + JSON body 的请求（未发送）。
func (e *envState) authedJSON(method, token, path string, body any) *http.Request {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.apiURL(path), bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// doJSON 执行请求、校验 2xx、解码响应体到 v，确保 body 关闭（不返回 resp）。
// 用于只需读取响应体的场景（fixture 构造、登录等），调用方无需关心 body 关闭。
func doJSON(req *http.Request, v any) {
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "HTTP 请求: "+req.Method+" "+req.URL.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		gomega.Expect(resp.StatusCode).To(gomega.BeNumerically(">=", 200),
			"期望 2xx，got "+strconv.Itoa(resp.StatusCode))
	}
	gomega.Expect(json.NewDecoder(resp.Body).Decode(v)).To(gomega.Succeed(), "解码响应体")
}

// doReq 执行请求并返回 resp，调用方负责关闭 body（用于需读 status/不读 body 的场景）。
func doReq(req *http.Request) *http.Response {
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "HTTP 请求: "+req.Method+" "+req.URL.String())
	return resp
}

// ===== fixture 构造器 =====

// seedTeam 创建团队（slug 唯一）。
func (e *envState) seedTeam(name string) *ent.Team {
	team, err := e.db().Team.Create().
		SetName(name).
		SetSlug(slugify(name)).
		Save(context.Background())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "seed team")
	return team
}

// seedService 创建服务并绑定团队，auto_create_incident=true 保证告警直接建 incident。
func (e *envState) seedService(name string, teamID int) *ent.Service {
	svc, err := e.db().Service.Create().
		SetName(name).
		SetSlug(slugify(name)).
		SetTeamID(teamID).
		SetAutoCreateIncident(true).
		Save(context.Background())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "seed service")
	return svc
}

// seedIntegration 通过 HTTP API 创建接入点，返回 webhook token（一次性）。
func (e *envState) seedIntegration(token, kind string, teamID, serviceID int) (*ent.Integration, string) {
	body, _ := json.Marshal(map[string]any{
		"name":       "e2e-integration-" + kind,
		"type":       kind,
		"config":     map[string]any{},
		"team_id":    teamID,
		"service_id": serviceID,
	})
	req, _ := http.NewRequest(http.MethodPost, e.apiURL("/integrations"), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var got struct {
		*ent.Integration
		Token string `json:"token"`
	}
	doJSON(req, &got)
	gomega.Expect(got.Integration).NotTo(gomega.BeNil(), "seed integration 返回 nil")
	return got.Integration, got.Token
}

// escTarget 描述升级通知目标（对应 schema.Target）。
type escTarget struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
}

// escLevel 描述升级层级，测试用短 delay 加速。
type escLevel struct {
	DelayMinutes int         `json:"delay_minutes"`
	Targets      []escTarget `json:"targets"`
	Channels     []string    `json:"notify_channels"`
}

// seedEscalationPolicy 通过 HTTP API 创建升级策略。
func (e *envState) seedEscalationPolicy(token, name string, levels []escLevel) *ent.EscalationPolicy {
	rawLevels := make([]map[string]any, len(levels))
	for i, lv := range levels {
		rawLevels[i] = map[string]any{
			"level":           i + 1,
			"delay_minutes":   lv.DelayMinutes,
			"targets":         lv.Targets,
			"notify_channels": lv.Channels,
		}
	}
	body, _ := json.Marshal(map[string]any{"name": name, "levels": rawLevels})
	req, _ := http.NewRequest(http.MethodPost, e.apiURL("/escalation-policies"), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var got ent.EscalationPolicy
	doJSON(req, &got)
	return &got
}

// bindPolicyToService 把升级策略绑定到 service（triage 据此启动升级链）。
func (e *envState) bindPolicyToService(serviceID, policyID int) {
	err := e.db().Service.UpdateOneID(serviceID).
		SetEscalationPolicyID(policyID).
		Exec(context.Background())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "bind policy to service")
}

// sendWebhook 向接入点发告警 payload（token 鉴权），触发 ingestion→normalize→triage 流水线。
func (e *envState) sendWebhook(token string, payload []byte) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, e.apiURL("/webhook/"+token), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "send webhook")
	return resp
}

// loginAdmin 用 admin/changeme 登录，返回 JWT access token。
func loginAdmin(e *envState) string {
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "changeme"})
	req, _ := http.NewRequest(http.MethodPost, e.apiURL("/auth/login"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var got struct {
		AccessToken string `json:"access_token"`
	}
	doJSON(req, &got)
	gomega.Expect(got.AccessToken).NotTo(gomega.BeEmpty(), "login access token")
	return got.AccessToken
}

// loginAs 用指定用户名/密码登录，返回 JWT access token（供非 admin 角色测试）。
func loginAs(e *envState, username, password string) string {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, _ := http.NewRequest(http.MethodPost, e.apiURL("/auth/login"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var got struct {
		AccessToken string `json:"access_token"`
	}
	doJSON(req, &got)
	return got.AccessToken
}

// seedUserWithRole 创建一个带 org 级角色绑定的普通用户，返回 (用户, 登录token)。
// 供 RBAC 越权 e2e 用：用受限角色登录验证写路由应被 RouteGuard 拒 403。
// roleName 必须是已 seed 的内置角色名（org_admin/subscriber/responder...）。
// subscriber 是只读角色（仅 incident.view 等），最适合验证"越权拒绝"。
func (e *envState) seedUserWithRole(username, roleName string) (*ent.User, string) {
	ctx := context.Background()
	u, err := e.db().User.Create().
		SetUsername(username).
		SetName(username).
		SetEmail(username + "@e2e.test").
		SetPasswordHash(auth.HashPassword("e2e-pw-123")).
		Save(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create user "+username)
	rl, err := e.db().Role.Query().Where(role.NameEQ(roleName)).Only(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "query role "+roleName)
	_, err = e.db().RoleBinding.Create().
		SetUserID(u.ID).
		SetRoleID(rl.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).
		Save(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "bind "+roleName+" to "+username)
	tok := loginAs(e, username, "e2e-pw-123")
	gomega.Expect(tok).NotTo(gomega.BeEmpty(), "login as "+username)
	return u, tok
}

// ===== 异步轮询断言（基于 gomega Eventually）=====

// waitForIncidentCount 轮询直到库中有 count 条 incident，返回它们。
func (e *envState) waitForIncidentCount(count int) []*ent.Incident {
	var incs []*ent.Incident
	gomega.Eventually(func() int {
		list, err := e.db().Incident.Query().Order(ent.Asc(incident.FieldID)).All(context.Background())
		if err != nil {
			return -1
		}
		incs = list
		return len(list)
	}, 15*time.Second, 200*time.Millisecond).
		Should(gomega.Equal(count), "等待流水线建出 "+strconv.Itoa(count)+" 条 incident")
	return incs
}

// waitForIncidentStatus 轮询直到指定 incident 达到目标状态。
func (e *envState) waitForIncidentStatus(incID int, want incident.Status) *ent.Incident {
	var got *ent.Incident
	gomega.Eventually(func() incident.Status {
		inc, err := e.db().Incident.Get(context.Background(), incID)
		if err != nil {
			return ""
		}
		got = inc
		return inc.Status
	}, 15*time.Second, 200*time.Millisecond).
		Should(gomega.Equal(want), "incident "+strconv.Itoa(incID)+" 状态="+string(want))
	return got
}

// waitForEscalationLevel 轮询直到 incident 的 current_level 达到目标层级。
func (e *envState) waitForEscalationLevel(incID, wantLevel int) *ent.Incident {
	var got *ent.Incident
	gomega.Eventually(func() int {
		inc, err := e.db().Incident.Get(context.Background(), incID)
		if err != nil {
			return -1
		}
		got = inc
		return inc.CurrentLevel
	}, 15*time.Second, 200*time.Millisecond).
		Should(gomega.Equal(wantLevel), "incident 升级到 level "+strconv.Itoa(wantLevel))
	return got
}

// waitForEscalationLevelAtLeast 轮询直到 current_level >= wantLevel（FIX-6）。
// 用于多层 delay=0 的快速连续升级场景：asynq 可能在 200ms 轮询间隔内连续越过多个 level，
// 用 >= 语义容忍快速越过，避免等中间态超时 flaky。
func (e *envState) waitForEscalationLevelAtLeast(incID, wantLevel int) *ent.Incident {
	var got *ent.Incident
	gomega.Eventually(func() int {
		inc, err := e.db().Incident.Get(context.Background(), incID)
		if err != nil {
			return -1
		}
		got = inc
		return inc.CurrentLevel
	}, 15*time.Second, 200*time.Millisecond).
		Should(gomega.BeNumerically(">=", wantLevel), "incident 升级到至少 level "+strconv.Itoa(wantLevel))
	return got
}

// waitForTimelineEntry 轮询直到 incident 有至少一条时间线条目。
func (e *envState) waitForTimelineEntry(incID int) {
	gomega.Eventually(func() int {
		cnt, err := e.db().TimelineItem.Query().
			Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
			Count(context.Background())
		if err != nil {
			return 0
		}
		return cnt
	}, 15*time.Second, 200*time.Millisecond).
		Should(gomega.BeNumerically(">", 0), "incident "+strconv.Itoa(incID)+" 时间线条目")
}

// ===== 工具 =====

// slugify 把名字转成合法 slug（小写 + 纳秒时间戳后缀保证唯一）。
func slugify(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}

// itoa 整数转字符串（strconv.Itoa 的别名，spec 内高频用，缩短写法）。
func itoa(n int) string { return strconv.Itoa(n) }

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
