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
	PermIncidentClose        Permission = "incident.close"
	PermIncidentReopen       Permission = "incident.reopen"
	PermIncidentReassign     Permission = "incident.reassign"
	PermIncidentSnooze       Permission = "incident.snooze"
	PermIncidentAddResponder Permission = "incident.add_responder"
	PermIncidentRunbookExec  Permission = "incident.runbook.execute"
	PermIncidentDelete       Permission = "incident.delete"

	// —— event 告警查看 / 投递 ——
	PermEventView         Permission = "event.view"
	PermEventViewUnrouted Permission = "event.view_unrouted"
	// event.create 开放 API 程序化投递 Event（T5.1，X-Vigil-Key 走同一分诊链路）。
	// 与 webhook 的 token 鉴权正交：webhook 是接入点自证，开放 API 是登录态/API Key 用户
	// 主动投递，须显式授权（避免任意登录用户凭 API Key 往任意接入点灌告警）。
	PermEventCreate Permission = "event.create"

	// —— raw_event 原始告警暂存（接入运维，T5.5）——
	// 查询/重放 parse_failed/requeued/received 的原始记录属接入排障面，
	// 与接入点管理同档（团队软隔离——只能看/重放自己团队接入点的 raw_event）。
	PermRawEventView   Permission = "raw_event.view"
	PermRawEventReplay Permission = "raw_event.replay"

	// —— webhook_delivery 出站 webhook 投递（死信运维，T5.2）——
	// 出站 URL 是全局配置式订阅（非 team 资源），故查询/重放死信是 org 级运维操作，
	// 不走团队软隔离——仅授予组织级管理角色（org_admin）。
	PermWebhookDeliveryView   Permission = "webhook_delivery.view"
	PermWebhookDeliveryReplay Permission = "webhook_delivery.replay"

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

	// —— ticket_integration 出向工单集成（能力域 14 出向，T4.3）——
	// 工单集成持外部系统凭据、决定复盘 ActionItem 往哪建单，管理面独立于入向接入点。
	PermTicketIntegrationView   Permission = "ticket_integration.view"
	PermTicketIntegrationCreate Permission = "ticket_integration.create"
	PermTicketIntegrationUpdate Permission = "ticket_integration.update"
	PermTicketIntegrationDelete Permission = "ticket_integration.delete"

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

	// —— analytics 报表与度量（能力域 11 分析）——
	// 报表为组织级视图（当前无团队 scope 隔离，见 docs/backlog.md），
	// 故仅授予 org 级角色，避免团队管理员越权看到全组织指标。
	PermAnalyticsView Permission = "analytics.view"

	// —— ai AI 洞察处置（能力域 11，human-in-the-loop）——
	// 采纳/拒绝 AI 建议是处置级动作（改判会影响后续自动化/复盘），
	// 不能挂只读的 incident.view（subscriber 也含），否则只读干系人可越权改判。
	PermAIInsightResolve Permission = "ai.insight.resolve"

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
	PermIncidentResolve, PermIncidentClose, PermIncidentReopen, PermIncidentReassign, PermIncidentSnooze,
	PermIncidentAddResponder, PermIncidentRunbookExec, PermIncidentDelete,
	PermEventView, PermEventViewUnrouted, PermEventCreate,
	PermRawEventView, PermRawEventReplay,
	PermWebhookDeliveryView, PermWebhookDeliveryReplay,
	PermServiceView, PermServiceCreate, PermServiceUpdate, PermServiceDelete, PermServiceRouteOverride,
	PermScheduleView, PermScheduleCreate, PermScheduleUpdate, PermScheduleDelete, PermScheduleOverride,
	PermEscalationView, PermEscalationCreate, PermEscalationUpdate, PermEscalationDelete,
	PermRunbookView, PermRunbookCreate, PermRunbookUpdate, PermRunbookDelete, PermRunbookExecute,
	PermIntegrationView, PermIntegrationCreate, PermIntegrationUpdate, PermIntegrationDelete,
	PermTicketIntegrationView, PermTicketIntegrationCreate, PermTicketIntegrationUpdate, PermTicketIntegrationDelete,
	PermPostmortemView, PermPostmortemCreate, PermPostmortemUpdate, PermPostmortemPublish, PermPostmortemActionItemManage,
	PermTeamView, PermTeamCreate, PermTeamUpdate, PermTeamDelete, PermTeamMemberManage,
	PermUserView, PermUserCreate, PermUserUpdate, PermUserDisable, PermUserIMBind,
	PermRoleView, PermRoleCreate, PermRoleUpdate, PermRoleDelete, PermRoleAssign,
	PermNotificationRuleView, PermNotificationRuleCreate, PermNotificationRuleUpdate, PermNotificationRuleDelete,
	PermNotificationTemplateView, PermNotificationTemplateCreate, PermNotificationTemplateUpdate, PermNotificationTemplateDelete,
	PermSuppressionView, PermSuppressionCreate, PermSuppressionUpdate, PermSuppressionDelete,
	PermAnalyticsView,
	PermAIInsightResolve,
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
