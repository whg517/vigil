/**
 * api —— 类型化后端 client，封装 lib/http 的 axios 实例。
 *
 * 对应后端路由（见 internal 下各包的 handler.go Register 方法）：
 *   GET  /incidents                list（?status=&severity=&limit=&offset=）
 *   GET  /incidents/:id            detail（含 responders/events）
 *   POST /incidents/:id/ack
 *   POST /incidents/:id/resolve
 *   POST /incidents/:id/escalate
 *   GET  /incidents/:id/timeline
 *   GET  /analytics/dashboard      仪表盘汇总（?days=7）
 *
 * 注：actor 由 http 拦截器经 X-Vigil-User-ID 注入（后端从鉴权 context 取），
 *     故写操作调用方无需传 actor_id。
 */
import { http } from "@/lib/http";
import type {
  ActionItem,
  APIKey,
  APIKeyCreated,
  AuditLogListResponse,
  DashboardMetrics,
  EscalationPolicy,
  Incident,
  Integration,
  IntegrationCreated,
  ListResponse,
  NotificationRule,
  NotificationTemplate,
  OncallResult,
  Postmortem,
  PreviewResult,
  Role,
  RoleBinding,
  Runbook,
  Schedule,
  Service,
  SuppressionRule,
  TimelineItem,
} from "@/lib/types";

export interface ListIncidentsParams {
  status?: string;
  severity?: string;
  limit?: number;
  offset?: number;
}

export const api = {
  // —— Incident ——
  listIncidents(params: ListIncidentsParams = {}) {
    return http
      .get<ListResponse<Incident>>("/incidents", { params })
      .then((r) => r.data);
  },

  getIncident(id: number) {
    return http.get<Incident>(`/incidents/${id}`).then((r) => r.data);
  },

  ackIncident(id: number) {
    // body 不再传 actor（后端从鉴权 context 取）；空 body 即可
    return http
      .post<Incident>(`/incidents/${id}/ack`, {})
      .then((r) => r.data);
  },

  resolveIncident(id: number) {
    return http
      .post<Incident>(`/incidents/${id}/resolve`, {})
      .then((r) => r.data);
  },

  escalateIncident(id: number) {
    return http
      .post<Incident>(`/incidents/${id}/escalate`, {})
      .then((r) => r.data);
  },

  // —— Timeline ——
  listTimeline(incidentId: number) {
    return http
      .get<ListResponse<TimelineItem>>(`/incidents/${incidentId}/timeline`)
      .then((r) => r.data);
  },

  // —— Analytics ——
  getDashboard(days = 7) {
    return http
      .get<DashboardMetrics>("/analytics/dashboard", { params: { days } })
      .then((r) => r.data);
  },

  // —— Service（能力域 4/13）——
  listServices() {
    return http.get<Service[]>("/services").then((r) => r.data);
  },
  getService(id: number) {
    return http.get<Service>(`/services/${id}`).then((r) => r.data);
  },
  createService(body: Partial<Service> & { name: string; slug: string }) {
    return http.post<Service>("/services", body).then((r) => r.data);
  },
  updateService(id: number, body: Partial<Service>) {
    return http.patch<Service>(`/services/${id}`, body).then((r) => r.data);
  },
  deleteService(id: number) {
    return http.delete(`/services/${id}`).then((r) => r.data);
  },
  // ===== Integration 接入点（能力域 1）=====
  listIntegrations() {
    return http.get<Integration[]>("/integrations").then((r) => r.data);
  },
  createIntegration(body: { name: string; type: string; config?: Record<string, unknown>; team_id?: number; service_id?: number }) {
    return http.post<IntegrationCreated>("/integrations", body).then((r) => r.data);
  },
  getIntegration(id: number) {
    return http.get<Integration>(`/integrations/${id}`).then((r) => r.data);
  },
  updateIntegration(id: number, body: { name?: string; enabled?: boolean }) {
    return http.patch<Integration>(`/integrations/${id}`, body).then((r) => r.data);
  },
  deleteIntegration(id: number) {
    return http.delete(`/integrations/${id}`).then((r) => r.data);
  },
  // ===== EscalationPolicy 升级策略（能力域 6）=====
  listEscalationPolicies() {
    return http.get<EscalationPolicy[]>("/escalation-policies").then((r) => r.data);
  },
  createEscalationPolicy(body: { name: string; repeat_times?: number; levels?: EscalationPolicy["levels"] }) {
    return http.post<EscalationPolicy>("/escalation-policies", body).then((r) => r.data);
  },
  getEscalationPolicy(id: number) {
    return http.get<EscalationPolicy>(`/escalation-policies/${id}`).then((r) => r.data);
  },
  updateEscalationPolicy(id: number, body: Partial<{ name: string; repeat_times: number; levels: EscalationPolicy["levels"] }>) {
    return http.patch<EscalationPolicy>(`/escalation-policies/${id}`, body).then((r) => r.data);
  },
  deleteEscalationPolicy(id: number) {
    return http.delete(`/escalation-policies/${id}`).then((r) => r.data);
  },

  // —— Schedule（能力域 5）——
  listSchedules() {
    return http.get<Schedule[]>("/schedules").then((r) => r.data);
  },
  getSchedule(id: number) {
    return http.get<Schedule>(`/schedules/${id}`).then((r) => r.data);
  },
  createSchedule(body: Partial<Schedule> & { name: string }) {
    return http.post<Schedule>("/schedules", body).then((r) => r.data);
  },
  updateSchedule(id: number, body: Partial<Schedule>) {
    return http.patch<Schedule>(`/schedules/${id}`, body).then((r) => r.data);
  },
  deleteSchedule(id: number) {
    return http.delete(`/schedules/${id}`).then((r) => r.data);
  },
  getOncall(id: number, time?: string) {
    return http
      .get<OncallResult>(`/schedules/${id}/oncall`, {
        params: time ? { time } : {},
      })
      .then((r) => r.data);
  },
  previewSchedule(id: number, days = 14) {
    return http
      .get<PreviewResult>(`/schedules/${id}/preview`, { params: { days } })
      .then((r) => r.data);
  },

  // —— Runbook（能力域 9）——
  listRunbooks() {
    return http.get<Runbook[]>("/runbooks").then((r) => r.data);
  },
  getRunbook(id: number) {
    return http.get<Runbook>(`/runbooks/${id}`).then((r) => r.data);
  },
  createRunbook(body: Partial<Runbook> & { name: string; type: Runbook["type"] }) {
    return http.post<Runbook>("/runbooks", body).then((r) => r.data);
  },
  deleteRunbook(id: number) {
    return http.delete(`/runbooks/${id}`).then((r) => r.data);
  },
  executeRunbook(id: number, body: { incident_id: number; approved?: boolean }) {
    return http
      .post<{ result?: string; error?: string }>(`/runbooks/${id}/execute`, body)
      .then((r) => r.data);
  },

  // —— Postmortem（能力域 12）——
  listPostmortems() {
    return http.get<Postmortem[]>("/postmortems").then((r) => r.data);
  },
  getPostmortem(id: number) {
    return http.get<Postmortem>(`/postmortems/${id}`).then((r) => r.data);
  },
  generatePostmortemDraft(incidentId: number) {
    return http
      .post<Postmortem>(`/incidents/${incidentId}/postmortem/draft`, {})
      .then((r) => r.data);
  },
  transitionPostmortem(id: number, status: Postmortem["status"]) {
    return http
      .patch<Postmortem>(`/postmortems/${id}/transition`, { status })
      .then((r) => r.data);
  },
  addActionItem(id: number, body: { description: string; owner_id?: string }) {
    return http
      .post<ActionItem>(`/postmortems/${id}/action-items`, body)
      .then((r) => r.data);
  },
  updateActionItem(id: number, body: Partial<ActionItem>) {
    return http.patch<ActionItem>(`/action-items/${id}`, body).then((r) => r.data);
  },

  // —— 通知规则（能力域 7）——
  listNotificationRules() {
    return http.get<NotificationRule[]>("/notification-rules").then((r) => r.data);
  },
  createNotificationRule(body: Partial<NotificationRule> & { name: string }) {
    return http.post<NotificationRule>("/notification-rules", body).then((r) => r.data);
  },
  updateNotificationRule(id: number, body: Partial<NotificationRule>) {
    return http.patch<NotificationRule>(`/notification-rules/${id}`, body).then((r) => r.data);
  },
  deleteNotificationRule(id: number) {
    return http.delete(`/notification-rules/${id}`).then((r) => r.data);
  },
  testNotificationRule(id: number, incidentId: number) {
    return http
      .post<{ quiet_hours_suppress?: boolean }>(`/notification-rules/${id}/test`, {}, { params: { incident_id: incidentId } })
      .then((r) => r.data);
  },

  // —— 抑制规则（能力域 3 M3.2）——
  listSuppressionRules() {
    return http.get<SuppressionRule[]>("/suppression-rules").then((r) => r.data);
  },
  createSuppressionRule(body: Partial<SuppressionRule> & { name: string }) {
    return http.post<SuppressionRule>("/suppression-rules", body).then((r) => r.data);
  },
  updateSuppressionRule(id: number, body: Partial<SuppressionRule>) {
    return http.patch<SuppressionRule>(`/suppression-rules/${id}`, body).then((r) => r.data);
  },
  deleteSuppressionRule(id: number) {
    return http.delete(`/suppression-rules/${id}`).then((r) => r.data);
  },

  // —— 通知模板（能力域 7 M7.5）——
  listNotificationTemplates() {
    return http.get<NotificationTemplate[]>("/notification-templates").then((r) => r.data);
  },
  createNotificationTemplate(body: Partial<NotificationTemplate> & { name: string; title_template: string }) {
    return http.post<NotificationTemplate>("/notification-templates", body).then((r) => r.data);
  },
  updateNotificationTemplate(id: number, body: Partial<NotificationTemplate>) {
    return http.patch<NotificationTemplate>(`/notification-templates/${id}`, body).then((r) => r.data);
  },
  deleteNotificationTemplate(id: number) {
    return http.delete(`/notification-templates/${id}`).then((r) => r.data);
  },
  previewTemplate(id: number, incidentId: number) {
    return http
      .post<{ title: string; body: string }>(`/notification-templates/${id}/preview`, {}, { params: { incident_id: incidentId } })
      .then((r) => r.data);
  },

  // —— RBAC（能力域 13）——
  listRoles() {
    return http.get<Role[]>("/roles").then((r) => r.data);
  },
  createRole(body: { name: string; description?: string; scope_level?: "org" | "team"; permissions?: string[] }) {
    return http.post<Role>("/roles", body).then((r) => r.data);
  },
  deleteRole(id: number) {
    return http.delete(`/roles/${id}`).then((r) => r.data);
  },
  listRoleBindings() {
    return http.get<RoleBinding[]>("/role-bindings").then((r) => r.data);
  },
  createRoleBinding(body: { user_id: number; role_id: number; scope_level?: "org" | "team"; team_id?: number; expires_in?: number }) {
    return http.post<RoleBinding>("/role-bindings", body).then((r) => r.data);
  },
  deleteRoleBinding(id: number) {
    return http.delete(`/role-bindings/${id}`).then((r) => r.data);
  },
  // ===== API Key（能力域 13 §API Key 管理）=====
  listAPIKeys() {
    return http.get<APIKey[]>("/api-keys").then((r) => r.data);
  },
  createAPIKey(body: { name: string; scope?: string[]; expires_in_hours?: number }) {
    return http.post<APIKeyCreated>("/api-keys", body).then((r) => r.data);
  },
  deleteAPIKey(id: number) {
    return http.delete(`/api-keys/${id}`).then((r) => r.data);
  },
  // ===== 审计日志（能力域 13 §审计日志，只读查询）=====
  listAuditLogs(params?: { actor_user_id?: number; action?: string; resource_type?: string; limit?: number; offset?: number }) {
    return http.get<AuditLogListResponse>("/audit-logs", { params }).then((r) => r.data);
  },
};
