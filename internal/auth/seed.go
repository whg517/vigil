// seed.go 内置角色种子数据（能力域 13 §5.4）。
//
// 对应 docs/data-model.md §5.4：系统出厂自带几个常用角色，
// builtin=true 可复制不可删。首次启动时通过 SeedBuiltinRoles 写入，
// 保证鉴权生效后有可用角色（否则所有人被拒）。
package auth

import (
	"context"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/role"
)

// builtinRoles 内置角色定义（name -> 权限点列表 + scope_level）。
// 对应 data-model.md §5.4 表格。
var builtinRoles = []struct {
	Name        string
	Description string
	Scope       role.ScopeLevel
	Permissions []string
}{
	{
		Name:        "org_admin",
		Description: "组织超管，拥有全部权限，仅谨慎授予",
		Scope:       role.ScopeLevelOrg,
		Permissions: allPermissionStrings(), // 全部权限点
	},
	{
		Name:        "team_admin",
		Description: "团队管理员，管理团队的服务/排班/升级/runbook/集成与成员",
		Scope:       role.ScopeLevelTeam,
		Permissions: concatPerms(
			// team 自管理 + 成员
			"team.view", "team.update", "team.member.manage",
			// 服务/集成（含未路由 Event 重路由：service.route_override，M6）
			"service.view", "service.create", "service.update", "service.delete", "service.route_override",
			"integration.view", "integration.create", "integration.update", "integration.delete",
			// 开放 API 投递 + 接入排障（原始告警查询/重放，T5.1/T5.5）
			"event.create", "raw_event.view", "raw_event.replay",
			// 出向工单集成（复盘 ActionItem 自动建单目标，T4.3，团队级配置）
			"ticket_integration.view", "ticket_integration.create", "ticket_integration.update", "ticket_integration.delete",
			// Runbook 执行器加密托管凭据（T6.3/S16，团队级配置）
			"credential.view", "credential.create", "credential.update", "credential.delete",
			// 排班/升级（含换他人班：schedule.override，C5/M5.3）
			"schedule.view", "schedule.create", "schedule.update", "schedule.delete", "schedule.override",
			"escalation.view", "escalation.create", "escalation.update", "escalation.delete",
			// runbook
			"runbook.view", "runbook.create", "runbook.update", "runbook.delete", "runbook.execute",
			// 事件全管理（团队范围内）
			"incident.view", "incident.create", "incident.ack", "incident.escalate",
			"incident.resolve", "incident.close", "incident.reopen", "incident.reassign", "incident.snooze",
			"incident.add_responder", "incident.runbook.execute", "incident.merge",
			// AI 建议采纳/拒绝（处置级，非只读）
			"ai.insight.resolve",
			// 事件查看
			"event.view",
			// 复盘
			"postmortem.view", "postmortem.create", "postmortem.update", "postmortem.publish", "postmortem.actionitem.manage",
			// 通知规则 + 模板 + 抑制规则（团队范围内全管理）
			"notification.rule.view", "notification.rule.create", "notification.rule.update", "notification.rule.delete",
			"notification.template.view", "notification.template.create", "notification.template.update", "notification.template.delete",
			"suppression.view", "suppression.create", "suppression.update", "suppression.delete",
			// 角色查看（不能改角色定义）
			"role.view",
			// 报表查看（S14：团队 scope 隔离后，Leader 看本团队报表）
			"analytics.view",
		),
	},
	{
		Name:        "responder",
		Description: "一线值班，处置事件",
		Scope:       role.ScopeLevelTeam,
		Permissions: []string{
			"incident.view", "incident.ack", "incident.escalate", "incident.resolve",
			"incident.reopen", "incident.snooze", "incident.add_responder", "incident.runbook.execute",
			"event.view",
			"runbook.view", "runbook.execute",
			"postmortem.view",
			"schedule.view",
			"service.view",
			// AI 建议采纳/拒绝（一线处置的一部分，human-in-the-loop）
			"ai.insight.resolve",
		},
	},
	{
		Name:        "responder_lead",
		Description: "值班长/技术负责人， responder 权限 + 指派 + 复盘编辑",
		Scope:       role.ScopeLevelTeam,
		Permissions: []string{
			"incident.view", "incident.ack", "incident.escalate", "incident.resolve",
			"incident.close", "incident.reopen", "incident.reassign", "incident.snooze", "incident.add_responder",
			"incident.runbook.execute", "incident.merge",
			"event.view",
			"runbook.view", "runbook.execute",
			"postmortem.view", "postmortem.create", "postmortem.update", "postmortem.publish",
			"schedule.view",
			"service.view",
			// AI 建议采纳/拒绝（处置级）
			"ai.insight.resolve",
			// 通知/抑制规则查看（lead 需理解为何被抑制/静默）
			"notification.rule.view", "notification.template.view", "suppression.view",
			// 报表查看（S14：团队 scope 隔离后，值班长看本团队报表）
			"analytics.view",
		},
	},
	{
		Name:        "subscriber",
		Description: "只读干系人（如业务方），只看不能改",
		Scope:       role.ScopeLevelTeam,
		Permissions: []string{
			"incident.view", "event.view", "postmortem.view",
		},
	},
	{
		Name:        "oncall",
		Description: "值班人， responder 权限 + 可为自己换班",
		Scope:       role.ScopeLevelTeam,
		Permissions: []string{
			"incident.view", "incident.ack", "incident.escalate", "incident.resolve",
			"event.view",
			"runbook.view", "runbook.execute",
			"schedule.view", "schedule.override",
		},
	},
}

// SeedBuiltinRoles 写入内置角色（幂等）。
// 在服务启动时调用，保证鉴权生效后有可用角色。
//
// 幂等策略：依赖 Role.name 唯一约束（schema 已加）。
// 直接 Create，遇到 ConstraintError（name 冲突）视为「已存在」跳过，
// 避免旧的「Count 判重 → Create」两步操作在多实例并发启动时产生竞态。
func SeedBuiltinRoles(ctx context.Context, db *ent.Client) error {
	for _, br := range builtinRoles {
		_, err := db.Role.Create().
			SetName(br.Name).
			SetDescription(br.Description).
			SetBuiltin(true).
			SetScopeLevel(br.Scope).
			SetPermissions(br.Permissions).
			Save(ctx)
		if err != nil {
			// 唯一约束冲突 = 已存在（并发或重复启动），幂等跳过
			if ent.IsConstraintError(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// allPermissionStrings 返回全部权限点的字符串形式（org_admin 用）。
func allPermissionStrings() []string {
	out := make([]string, 0, len(AllPermissions))
	for _, p := range AllPermissions {
		out = append(out, string(p))
	}
	return out
}

// concatPerms 把可变字符串参数拼成切片。
func concatPerms(perms ...string) []string {
	return perms
}
