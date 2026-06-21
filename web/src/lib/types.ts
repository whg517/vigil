/**
 * 类型定义 —— 对齐后端 ent JSON 输出与 analytics 度量结构。
 * 字段来源：ent/schema/*.go（实体）与 internal/analytics/engine.go（度量）。
 * 仅声明本 MVP 用到的子集，按需扩展。
 */

// —— Incident（ent/schema/incident.go）——
export type Severity = "critical" | "warning" | "info";
// 状态机：triggered → escalated → acked → resolved → closed
export type IncidentStatus =
  | "triggered"
  | "escalated"
  | "acked"
  | "resolved"
  | "closed";
export type Priority = "p1" | "p2" | "p3" | "p4";

export interface Incident {
  id: number;
  number: string; // 人类可读编号 INC-xxxx
  title: string;
  severity: Severity;
  status: IncidentStatus;
  priority: Priority;
  summary?: string;
  escalated_count: number;
  current_level: number;
  merged_into?: string;
  trigger_type?: "auto" | "manual" | "merged";
  trigger_source_event_id?: string;
  war_room?: Record<string, unknown>;
  resolved_at?: string | null;
  closed_at?: string | null;
  created_at: string;
  updated_at: string;
  // 边（按需 WithX 才出现）
  responders?: User[];
  events?: Event[];
}

// —— TimelineItem（ent/schema/timeline_action.go）——
export type TimelineType =
  | "incident_created"
  | "event_attached"
  | "status_changed"
  | "escalated"
  | "ack"
  | "resolved"
  | "reopened"
  | "responder_added"
  | "note_added"
  | "runbook_executed"
  | "ai_insight"
  | "im_message";

export interface TimelineActor {
  kind?: string; // system | user | integration | ai
  id?: string;
  name?: string;
}

export interface TimelineItem {
  id: number;
  timestamp: string;
  type: TimelineType;
  actor: TimelineActor;
  content: string;
  detail?: Record<string, unknown>;
  source: "web" | "im" | "api" | "system" | "ai";
  created_at: string;
}

// —— 通用 list 响应（后端统一格式）——
export interface ListResponse<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

// —— 引用实体（最小字段）——
export interface User {
  id: number;
  name: string;
  username?: string;
  email?: string;
}

export interface Event {
  id: number;
  source_event_id: string;
  source: string;
  severity: Severity;
  status: string; // firing | resolved
  summary: string;
  is_noise?: boolean;
  received_at: string;
}

// —— Analytics（internal/analytics/engine.go）——
export interface AlertMetrics {
  Total: number;
  Notified: number;
  NoiseRate: number;
  Unrouted: number;
}

export interface IncidentMetrics {
  Total: number;
  BySeverity: Record<string, number>;
  ByStatus: Record<string, number>;
  MTTARatio: number;
  MTTRatio: number;
  ResolvedCount: number;
}

export interface TeamLoad {
  TeamID: number;
  TeamName: string;
  Incidents: number;
}

export interface PostmortemMetrics {
  Total: number;
  Published: number;
  CompletionRate: number;
}

export interface DashboardMetrics {
  Alert: AlertMetrics;
  Incident: IncidentMetrics;
  Load: TeamLoad[];
  Postmortem: PostmortemMetrics;
}

// —— Service（ent/schema/service.go，能力域 4/13）——
export interface Service {
  id: number;
  name: string;
  slug: string;
  description?: string;
  labels?: Record<string, string>;
  auto_create_incident: boolean;
  status: "active" | "disabled";
  created_at: string;
  updated_at: string;
}

// —— Schedule（ent/schema/schedule.go，能力域 5）——
export type ScheduleType = "calendar" | "rotation" | "follow_the_sun";
export interface ScheduleLayer {
  id: string;
  name: string;
  priority: number;
  rotation_id: string;
}
export interface Schedule {
  id: number;
  name: string;
  type: ScheduleType;
  timezone: string;
  layers?: ScheduleLayer[];
  created_at: string;
  updated_at: string;
}

// 排班查询：某时刻在班人
export interface OncallLayer {
  name: string;
  priority: number;
  users: { id: number; name: string }[];
}
export interface OncallResult {
  layers: OncallLayer[];
}
// 预览：日期 → 在班人
export interface PreviewResult {
  schedule_id: number;
  days: Record<string, { users: { id: number; name: string }[] }>;
}

// —— Runbook（ent/schema/runbook.go，能力域 9）——
export type RunbookType = "document" | "executable";
export interface Runbook {
  id: number;
  name: string;
  type: RunbookType;
  trigger?: Record<string, unknown>;
  content_markdown?: string;
  steps?: unknown[];
  created_at: string;
  updated_at: string;
}

// —— Postmortem（ent/schema/postmortem.go，能力域 12）——
export type PostmortemStatus =
  | "draft"
  | "in_review"
  | "published"
  | "archived";
export interface ActionItem {
  id: number;
  title: string;
  status: "open" | "in_progress" | "done" | "skipped";
  owner_id?: string;
  tracker_url?: string;
  created_at: string;
}
export interface Postmortem {
  id: number;
  incident_id: number;
  status: PostmortemStatus;
  sections?: Record<string, string>;
  created_at: string;
  updated_at: string;
  // 详情查询时带
  action_items?: ActionItem[];
}

// —— 通知规则（ent/schema/escalation_policy.go NotificationRule，能力域 7）——
export interface NotificationRule {
  id: number;
  name: string;
  condition?: Record<string, unknown>;
  channels: string[];
  template_id?: string;
  quiet_hours?: Record<string, unknown>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

// —— 抑制规则（ent/schema/suppression_rule.go，能力域 3 M3.2）——
export type SuppressionAction = "suppress" | "reduce_severity";
export interface SuppressionRule {
  id: number;
  name: string;
  match_labels: Record<string, string>;
  time_window?: Record<string, unknown>;
  severity_filter?: string[];
  action: SuppressionAction;
  reduce_to?: string;
  preserve_critical: boolean;
  enabled: boolean;
  expires_at?: string | null;
  created_at: string;
  updated_at: string;
}

// —— 通知模板（ent/schema/notification_template.go，能力域 7 M7.5）——
export type NotificationChannel = "im" | "email" | "webhook" | "phone" | "sms";
export type TemplateFormat = "text" | "interactive_card";
export interface NotificationTemplate {
  id: number;
  name: string;
  channel: NotificationChannel;
  format: TemplateFormat;
  title_template: string;
  body_template: string;
  actions?: { type: string; label: string }[];
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

// —— RBAC（能力域 13）——
export interface Role {
  id: number;
  name: string;
  description?: string;
  builtin: boolean;
  scope_level: "org" | "team";
  permissions: string[];
}
export interface RoleBinding {
  id: number;
  user_id: number;
  role_id: number;
  scope_level?: "org" | "team";
  team_id?: number;
  expires_at?: string | null;
}

/** API Key（能力域 13 §API Key 管理）—— 列表视图不含明文 token */
export interface APIKey {
  id: number;
  name: string;
  prefix: string;
  scope?: string[];
  status: "active" | "disabled";
  expires_at?: string | null;
  last_used_at?: string | null;
  created_at: string;
}

/** 创建 API Key 响应（含一次性明文 token，仅创建时返回） */
export interface APIKeyCreated extends APIKey {
  token: string;
}

/** 审计日志条目（能力域 13 §审计日志）—— 只读 */
export interface AuditLog {
  id: number;
  actor_user_id: number;
  actor_name: string;
  action: string;
  resource_type: string;
  resource_id: number;
  resource_name?: string;
  result: "success" | "failed" | "denied";
  detail?: Record<string, unknown>;
  ip?: string;
  user_agent?: string;
  created_at: string;
}

/** 审计日志列表响应（含分页元数据） */
export interface AuditLogListResponse {
  items: AuditLog[];
  total: number;
  limit: number;
  offset: number;
}
