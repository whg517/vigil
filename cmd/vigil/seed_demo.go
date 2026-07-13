package main

// seed-demo 子命令：一键灌演示数据，解决「新人跑通部署后面对空系统」的冷启动问题。
//
// 种子链路（内置 seed）只建 admin + 内置角色；本命令补齐一套最小但完整的演示对象：
// 团队 → 值班用户 → 排班（含轮换）→ 升级策略 → 服务 → webhook 接入点（含 token），
// 并打印可直接执行的 curl 命令，让用户向 webhook 入口发几条不同 severity 的模拟告警。
//
// ★ 保持流水线真实性：不直插 Incident——告警经 curl 走完整的
// ingestion → normalize → triage → escalation 流水线，演示的就是真实链路。
//
// 幂等：所有对象按唯一键（slug/username/名称+归属）先查后建，重复执行不重复建；
// 已存在的接入点复用原 token，curl 命令保持有效。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	entescalationpolicy "github.com/kevin/vigil/ent/escalationpolicy"
	entintegration "github.com/kevin/vigil/ent/integration"
	entrotation "github.com/kevin/vigil/ent/rotation"
	entschedule "github.com/kevin/vigil/ent/schedule"
	"github.com/kevin/vigil/ent/schema"
	entservice "github.com/kevin/vigil/ent/service"
	entteam "github.com/kevin/vigil/ent/team"
	entuser "github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/config"
)

// 演示对象的固定标识（幂等查找键）。demo- 前缀让演示数据一眼可辨、可整体清理。
const (
	demoTeamSlug        = "demo-team"
	demoServiceSlug     = "demo-orders"
	demoScheduleName    = "演示排班"
	demoPolicyName      = "演示升级策略"
	demoIntegrationName = "演示 Webhook 接入"
)

// demoUsers 演示值班用户（轮班参与者）。
// 刻意不设密码：这些账号只作排班/升级的通知目标，不开放密码登录——
// 避免 seed 出「已知口令」账号成为安全隐患（password_hash 为空时密码登录被拒绝）。
var demoUsers = []struct {
	username, name, email string
}{
	{"demo-zhang", "张演示", "demo-zhang@vigil.local"},
	{"demo-li", "李演示", "demo-li@vigil.local"},
}

// runSeedDemoCmd 执行 seed-demo 子命令：连库 → 幂等建演示对象 → 打印模拟告警 curl。
func runSeedDemoCmd() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return fmt.Errorf("open ent db: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 前置探测：表不存在说明尚未迁移，给出明确指引而非裸抛 SQL 错误。
	if _, err := db.Team.Query().Count(ctx); err != nil {
		return fmt.Errorf("查询失败（数据库可能尚未迁移，请先执行 `vigil migrate` 或 `make migrate`）: %w", err)
	}

	if err := seedDemo(ctx, db, baseURLFromAddr(cfg.HTTP.Addr)); err != nil {
		return err
	}
	return nil
}

// seedDemo 幂等创建演示对象并打印结果与 curl 命令。
// 创建顺序按依赖排列：team → users → schedule → policy → service → integration。
func seedDemo(ctx context.Context, db *ent.Client, baseURL string) error {
	fmt.Println("== Vigil 演示数据（幂等，重复执行不重复建）==")

	team, err := ensureDemoTeam(ctx, db)
	if err != nil {
		return err
	}
	users, err := ensureDemoUsers(ctx, db, team)
	if err != nil {
		return err
	}
	sched, err := ensureDemoSchedule(ctx, db, team, users)
	if err != nil {
		return err
	}
	policy, err := ensureDemoPolicy(ctx, db, team, sched)
	if err != nil {
		return err
	}
	svc, err := ensureDemoService(ctx, db, team, policy, sched)
	if err != nil {
		return err
	}
	integ, err := ensureDemoIntegration(ctx, db, team, svc)
	if err != nil {
		return err
	}

	printDemoCurls(baseURL, integ.Token)
	return nil
}

// ensureDemoTeam 按 slug 幂等创建演示团队。
func ensureDemoTeam(ctx context.Context, db *ent.Client) (*ent.Team, error) {
	team, err := db.Team.Query().Where(entteam.SlugEQ(demoTeamSlug)).Only(ctx)
	if err == nil {
		logExist("团队", team.Name+" ("+demoTeamSlug+")")
		return team, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query demo team: %w", err)
	}
	team, err = db.Team.Create().
		SetName("演示团队").
		SetSlug(demoTeamSlug).
		SetDescription("seed-demo 创建的演示团队，可安全删除").
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create demo team: %w", err)
	}
	logCreated("团队", team.Name+" ("+demoTeamSlug+")")
	return team, nil
}

// ensureDemoUsers 按 username 幂等创建演示值班用户，并保证团队成员关系（也幂等）。
func ensureDemoUsers(ctx context.Context, db *ent.Client, team *ent.Team) ([]*ent.User, error) {
	out := make([]*ent.User, 0, len(demoUsers))
	for _, du := range demoUsers {
		u, err := db.User.Query().Where(entuser.UsernameEQ(du.username)).Only(ctx)
		switch {
		case err == nil:
			logExist("用户", du.name+" ("+du.username+")")
		case ent.IsNotFound(err):
			u, err = db.User.Create().
				SetUsername(du.username).
				SetName(du.name).
				SetEmail(du.email).
				SetStatus(entuser.StatusActive).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("create demo user %s: %w", du.username, err)
			}
			logCreated("用户", du.name+" ("+du.username+")")
		default:
			return nil, fmt.Errorf("query demo user %s: %w", du.username, err)
		}
		// 团队成员关系幂等：先查边是否存在，避免多对多重复插入报唯一约束错。
		inTeam, err := u.QueryTeams().Where(entteam.IDEQ(team.ID)).Exist(ctx)
		if err != nil {
			return nil, fmt.Errorf("query team membership %s: %w", du.username, err)
		}
		if !inTeam {
			if err := u.Update().AddTeamIDs(team.ID).Exec(ctx); err != nil {
				return nil, fmt.Errorf("add %s to demo team: %w", du.username, err)
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// ensureDemoSchedule 按「名称 + 归属团队」幂等创建演示排班（rotation 型，24h 轮换）。
// 与 internal/schedule 的 create handler 同款结构：Rotation 实体承载参与者，
// Schedule.layers JSON 以 rotation_id 关联层——只写 layers 不建 Rotation 会导致
// Oncall 解算不到任何在班人。事务保证 Schedule 与 Rotation 的一致性。
func ensureDemoSchedule(ctx context.Context, db *ent.Client, team *ent.Team, users []*ent.User) (*ent.Schedule, error) {
	sched, err := db.Schedule.Query().
		Where(entschedule.NameEQ(demoScheduleName), entschedule.HasTeamWith(entteam.IDEQ(team.ID))).
		Only(ctx)
	if err == nil {
		logExist("排班", demoScheduleName)
		return sched, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query demo schedule: %w", err)
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	userIDs := make([]int, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
	}
	// start_date 取当天零点（排班时区），班次边界落在交接时刻，演示时序直观。
	loc, lerr := time.LoadLocation("Asia/Shanghai")
	if lerr != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	rot, err := tx.Rotation.Create().
		SetName("一线").
		SetRotationType(entrotation.RotationTypeDaily).
		SetShiftLength("24h").
		SetHandoffTime("09:00").
		SetStartDate(startOfDay).
		AddParticipantIDs(userIDs...).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("create demo rotation: %w", err)
	}
	sched, err = tx.Schedule.Create().
		SetName(demoScheduleName).
		SetType(entschedule.TypeRotation).
		SetTimezone("Asia/Shanghai").
		SetLayers([]schema.ScheduleLayer{{
			ID:         strconv.Itoa(rot.ID),
			Name:       "一线",
			Priority:   1,
			RotationID: strconv.Itoa(rot.ID),
		}}).
		SetTeamID(team.ID).
		AddRotationIDs(rot.ID).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("create demo schedule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit demo schedule: %w", err)
	}
	logCreated("排班", demoScheduleName+"（一线 24h 轮换：张演示 ⇄ 李演示）")
	return sched, nil
}

// ensureDemoPolicy 按「名称 + 归属团队」幂等创建两级演示升级策略：
// L1（0min）通知排班在班人 → 未 ack 5min 后 L2 通知全团队。
func ensureDemoPolicy(ctx context.Context, db *ent.Client, team *ent.Team, sched *ent.Schedule) (*ent.EscalationPolicy, error) {
	policy, err := db.EscalationPolicy.Query().
		Where(entescalationpolicy.NameEQ(demoPolicyName), entescalationpolicy.HasTeamWith(entteam.IDEQ(team.ID))).
		Only(ctx)
	if err == nil {
		logExist("升级策略", demoPolicyName)
		return policy, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query demo policy: %w", err)
	}
	policy, err = db.EscalationPolicy.Create().
		SetName(demoPolicyName).
		SetLevels([]schema.EscalationLevel{
			{
				Level:         1,
				DelayMinutes:  0,
				Targets:       []schema.Target{{Type: "schedule", TargetID: strconv.Itoa(sched.ID)}},
				NotifyChannel: []string{"im", "email"},
			},
			{
				Level:         2,
				DelayMinutes:  5,
				Targets:       []schema.Target{{Type: "team", TargetID: strconv.Itoa(team.ID)}},
				NotifyChannel: []string{"im", "email"},
			},
		}).
		SetTeamID(team.ID).
		AddScheduleIDs(sched.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create demo policy: %w", err)
	}
	logCreated("升级策略", demoPolicyName+"（L1 排班在班人 → 5min 未 ack → L2 全团队）")
	return policy, nil
}

// ensureDemoService 按 slug 幂等创建演示服务，绑定团队/升级策略/排班。
// labels.service=demo-orders 使 curl 告警经分诊 slug 直达路由命中本服务。
func ensureDemoService(ctx context.Context, db *ent.Client, team *ent.Team, policy *ent.EscalationPolicy, sched *ent.Schedule) (*ent.Service, error) {
	svc, err := db.Service.Query().Where(entservice.SlugEQ(demoServiceSlug)).Only(ctx)
	if err == nil {
		logExist("服务", svc.Name+" ("+demoServiceSlug+")")
		return svc, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query demo service: %w", err)
	}
	svc, err = db.Service.Create().
		SetName("演示订单服务").
		SetSlug(demoServiceSlug).
		SetDescription("seed-demo 创建的演示服务，可安全删除").
		SetLabels(map[string]string{"service": demoServiceSlug}).
		SetAutoCreateIncident(true).
		SetTeamID(team.ID).
		SetEscalationPolicyID(policy.ID).
		AddScheduleIDs(sched.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create demo service: %w", err)
	}
	logCreated("服务", svc.Name+" ("+demoServiceSlug+")，已绑升级策略与排班")
	return svc, nil
}

// ensureDemoIntegration 按「名称 + 归属团队」幂等创建 webhook 接入点。
// token 与 internal/integration 同款格式（vig_int_ 前缀 + 16 字节 hex）；
// 已存在时复用原 token，保证之前打印的 curl 命令持续有效。
func ensureDemoIntegration(ctx context.Context, db *ent.Client, team *ent.Team, svc *ent.Service) (*ent.Integration, error) {
	integ, err := db.Integration.Query().
		Where(entintegration.NameEQ(demoIntegrationName), entintegration.HasTeamWith(entteam.IDEQ(team.ID))).
		Only(ctx)
	if err == nil {
		logExist("接入点", demoIntegrationName)
		return integ, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query demo integration: %w", err)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	integ, err = db.Integration.Create().
		SetName(demoIntegrationName).
		SetType(entintegration.TypeWebhook).
		SetToken("vig_int_" + hex.EncodeToString(buf)).
		SetEnabled(true).
		SetTeamID(team.ID).
		SetServiceID(svc.ID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create demo integration: %w", err)
	}
	logCreated("接入点", demoIntegrationName+"（type=webhook）")
	return integ, nil
}

// printDemoCurls 打印可直接执行的模拟告警 curl 命令（三条不同 severity）。
// id 带时间戳后缀：重复演示时避开去重窗口（同 dedup_key 5min 内会被直接丢弃）。
func printDemoCurls(baseURL, token string) {
	webhookURL := baseURL + "/api/v1/webhook/" + token
	ts := time.Now().Unix()
	alerts := []struct {
		severity, summary string
	}{
		{"critical", "[演示] 订单服务 5xx 错误率超阈值"},
		{"warning", "[演示] 订单服务 P99 延迟升高"},
		{"info", "[演示] 订单服务发布完成"},
	}
	fmt.Println()
	fmt.Println("== 模拟告警：复制执行以下 curl，让告警走完整 接入→归一化→分诊→升级 流水线 ==")
	for _, a := range alerts {
		fmt.Println()
		fmt.Printf("curl -s -X POST '%s' \\\n", webhookURL)
		fmt.Printf("  -H 'Content-Type: application/json' \\\n")
		fmt.Printf("  -d '{\"id\":\"demo-%s-%d\",\"severity\":\"%s\",\"status\":\"firing\",\"summary\":\"%s\",\"labels\":{\"service\":\"%s\"}}'\n",
			a.severity, ts, a.severity, a.summary, demoServiceSlug)
	}
	fmt.Println()
	fmt.Printf("发送后打开 %s 查看 Incident（默认管理员 admin / changeme，首登需改密）。\n", baseURL)
	fmt.Println("提示：critical/warning 会自动建 Incident 并启动升级链；未配 IM/SMTP 时通知降级为日志，流水线本身不受影响。")
}

// baseURLFromAddr 把监听地址转成可访问的基地址（":8080" → "http://localhost:8080"）。
func baseURLFromAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

// logCreated / logExist 统一输出格式，让「新建 / 已存在」一目了然。
func logCreated(kind, desc string) { fmt.Printf("  [新建] %s：%s\n", kind, desc) }
func logExist(kind, desc string)   { fmt.Printf("  [已存在] %s：%s\n", kind, desc) }
