// Package auth 定义 Vigil 的 RBAC 权限模型。
//
// 权限点是系统内置的细粒度动作枚举（系统能力边界，固定），
// 命名规范 <resource>.<action>。角色（Role）由使用者自由组合这些权限点配置。
// 详见 data-model.md §5。
package auth

// Permission 权限点类型。
type Permission string

const (
	// —— incident 事件全生命周期 ——
	PermIncidentView         Permission = "incident.view"
	PermIncidentCreate       Permission = "incident.create"
	PermIncidentAck          Permission = "incident.ack"
	PermIncidentEscalate     Permission = "incident.escalate"
	PermIncidentResolve      Permission = "incident.resolve"
	PermIncidentReopen       Permission = "incident.reopen"
	PermIncidentReassign     Permission = "incident.reassign"
	PermIncidentSnooze       Permission = "incident.snooze"
	PermIncidentAddResponder Permission = "incident.add_responder"
	PermIncidentRunbookExec  Permission = "incident.runbook.execute"
	PermIncidentDelete       Permission = "incident.delete"

	// —— event 告警查看 ——
	PermEventView         Permission = "event.view"
	PermEventViewUnrouted Permission = "event.view_unrouted"

	// —— service 服务管理 ——
	PermServiceView          Permission = "service.view"
	PermServiceCreate        Permission = "service.create"
	PermServiceUpdate        Permission = "service.update"
	PermServiceDelete        Permission = "service.delete"
	PermServiceRouteOverride Permission = "service.route_override"

	// —— schedule 排班 ——
	PermScheduleView     Permission = "schedule.view"
	PermScheduleCreate   Permission = "schedule.create"
	PermScheduleUpdate   Permission = "schedule.update"
	PermScheduleDelete   Permission = "schedule.delete"
	PermScheduleOverride Permission = "schedule.override"

	// —— escalation 升级策略 ——
	PermEscalationView   Permission = "escalation.view"
	PermEscalationCreate Permission = "escalation.create"
	PermEscalationUpdate Permission = "escalation.update"
	PermEscalationDelete Permission = "escalation.delete"

	// —— runbook 处置手册 ——
	PermRunbookView    Permission = "runbook.view"
	PermRunbookCreate  Permission = "runbook.create"
	PermRunbookUpdate  Permission = "runbook.update"
	PermRunbookDelete  Permission = "runbook.delete"
	PermRunbookExecute Permission = "runbook.execute"

	// —— integration 接入点 ——
	PermIntegrationView   Permission = "integration.view"
	PermIntegrationCreate Permission = "integration.create"
	PermIntegrationUpdate Permission = "integration.update"
	PermIntegrationDelete Permission = "integration.delete"

	// —— postmortem 复盘 ——
	PermPostmortemView             Permission = "postmortem.view"
	PermPostmortemCreate           Permission = "postmortem.create"
	PermPostmortemUpdate           Permission = "postmortem.update"
	PermPostmortemPublish          Permission = "postmortem.publish"
	PermPostmortemActionItemManage Permission = "postmortem.actionitem.manage"

	// —— team 团队 ——
	PermTeamView         Permission = "team.view"
	PermTeamCreate       Permission = "team.create"
	PermTeamUpdate       Permission = "team.update"
	PermTeamDelete       Permission = "team.delete"
	PermTeamMemberManage Permission = "team.member.manage"

	// —— user 用户 ——
	PermUserView    Permission = "user.view"
	PermUserCreate  Permission = "user.create"
	PermUserUpdate  Permission = "user.update"
	PermUserDisable Permission = "user.disable"
	PermUserIMBind  Permission = "user.im.bind"

	// —— role 角色（管理角色定义本身）——
	PermRoleView   Permission = "role.view"
	PermRoleCreate Permission = "role.create"
	PermRoleUpdate Permission = "role.update"
	PermRoleDelete Permission = "role.delete"
	PermRoleAssign Permission = "role.assign"

	// —— notification 通知规则 / 模板 ——
	PermNotificationRuleView       Permission = "notification.rule.view"
	PermNotificationRuleCreate     Permission = "notification.rule.create"
	PermNotificationRuleUpdate     Permission = "notification.rule.update"
	PermNotificationRuleDelete     Permission = "notification.rule.delete"
	PermNotificationTemplateView   Permission = "notification.template.view"
	PermNotificationTemplateCreate Permission = "notification.template.create"
	PermNotificationTemplateUpdate Permission = "notification.template.update"
	PermNotificationTemplateDelete Permission = "notification.template.delete"

	// —— suppression 抑制规则（能力域 3 M3.2）——
	PermSuppressionView   Permission = "suppression.view"
	PermSuppressionCreate Permission = "suppression.create"
	PermSuppressionUpdate Permission = "suppression.update"
	PermSuppressionDelete Permission = "suppression.delete"

	// —— admin 平台级管理 ——
	PermAdminSettings          Permission = "admin.settings"
	PermAdminAuditView         Permission = "admin.audit.view"
	PermAdminAPIKeyManage      Permission = "admin.apikey.manage"
	PermAdminGlobalIntegration Permission = "admin.global_integration"
)

// AllPermissions 系统全部权限点（系统能力边界）。
// 角色配置权限时，必须从此集合中选取。
var AllPermissions = []Permission{
	PermIncidentView, PermIncidentCreate, PermIncidentAck, PermIncidentEscalate,
	PermIncidentResolve, PermIncidentReopen, PermIncidentReassign, PermIncidentSnooze,
	PermIncidentAddResponder, PermIncidentRunbookExec, PermIncidentDelete,
	PermEventView, PermEventViewUnrouted,
	PermServiceView, PermServiceCreate, PermServiceUpdate, PermServiceDelete, PermServiceRouteOverride,
	PermScheduleView, PermScheduleCreate, PermScheduleUpdate, PermScheduleDelete, PermScheduleOverride,
	PermEscalationView, PermEscalationCreate, PermEscalationUpdate, PermEscalationDelete,
	PermRunbookView, PermRunbookCreate, PermRunbookUpdate, PermRunbookDelete, PermRunbookExecute,
	PermIntegrationView, PermIntegrationCreate, PermIntegrationUpdate, PermIntegrationDelete,
	PermPostmortemView, PermPostmortemCreate, PermPostmortemUpdate, PermPostmortemPublish, PermPostmortemActionItemManage,
	PermTeamView, PermTeamCreate, PermTeamUpdate, PermTeamDelete, PermTeamMemberManage,
	PermUserView, PermUserCreate, PermUserUpdate, PermUserDisable, PermUserIMBind,
	PermRoleView, PermRoleCreate, PermRoleUpdate, PermRoleDelete, PermRoleAssign,
	PermNotificationRuleView, PermNotificationRuleCreate, PermNotificationRuleUpdate, PermNotificationRuleDelete,
	PermNotificationTemplateView, PermNotificationTemplateCreate, PermNotificationTemplateUpdate, PermNotificationTemplateDelete,
	PermSuppressionView, PermSuppressionCreate, PermSuppressionUpdate, PermSuppressionDelete,
	PermAdminSettings, PermAdminAuditView, PermAdminAPIKeyManage, PermAdminGlobalIntegration,
}

// IsValid 校验权限点是否合法（角色配置时用）。
func (p Permission) IsValid() bool {
	for _, perm := range AllPermissions {
		if perm == p {
			return true
		}
	}
	return false
}
