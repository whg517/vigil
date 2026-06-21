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
			// 服务/集成
			"service.view", "service.create", "service.update", "service.delete",
			"integration.view", "integration.create", "integration.update", "integration.delete",
			// 排班/升级
			"schedule.view", "schedule.create", "schedule.update", "schedule.delete",
			"escalation.view", "escalation.create", "escalation.update", "escalation.delete",
			// runbook
			"runbook.view", "runbook.create", "runbook.update", "runbook.delete", "runbook.execute",
			// 事件全管理（团队范围内）
			"incident.view", "incident.create", "incident.ack", "incident.escalate",
			"incident.resolve", "incident.reopen", "incident.reassign", "incident.snooze",
			"incident.add_responder", "incident.runbook.execute",
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
		},
	},
	{
		Name:        "responder_lead",
		Description: "值班长/技术负责人， responder 权限 + 指派 + 复盘编辑",
		Scope:       role.ScopeLevelTeam,
		Permissions: []string{
			"incident.view", "incident.ack", "incident.escalate", "incident.resolve",
			"incident.reopen", "incident.reassign", "incident.snooze", "incident.add_responder",
			"incident.runbook.execute",
			"event.view",
			"runbook.view", "runbook.execute",
			"postmortem.view", "postmortem.create", "postmortem.update", "postmortem.publish",
			"schedule.view",
			"service.view",
			// 通知/抑制规则查看（lead 需理解为何被抑制/静默）
			"notification.rule.view", "notification.template.view", "suppression.view",
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
