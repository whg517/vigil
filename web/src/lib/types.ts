/**
 * 类型定义 —— 从 OpenAPI spec（src/lib/api/types.gen.ts）派生的薄别名层。
 *
 * 权威源是后端 handler 注解经 swag 重新生成的 OpenAPI 3.1 spec；
 * 本文件只做两件事：
 *   1. 给丑陋的生成 schema 名（ent.Incident、internal_analytics.AlertMetrics…）起易用别名；
 *   2. 用 Required<> 把 ent 实体的可选字段标为必填（ent JSON 总会输出这些字段，
 *      swag 无法推断 required-ness，故生成端一律 optional，这里收紧）。
 *
 * 不要在此手写字段；如需新增/修改字段，改后端注解后 `pnpm gen:types` 重生成。
 */
import type { components } from "@/lib/api/types.gen";

type Schemas = components["schemas"];

// —— Incident（ent/schema/incident.go）——
export type Severity = Schemas["github_com_kevin_vigil_ent_incident.Severity"];
// 状态机：triggered → escalated → acked → resolved → closed
export type IncidentStatus = Schemas["github_com_kevin_vigil_ent_incident.Status"];
export type Priority = Schemas["github_com_kevin_vigil_ent_incident.Priority"];

export type Incident = Required<
  Omit<Schemas["ent.Incident"], "edges" | "embedding" | "war_room">
> & {
  summary?: string;
  merged_into?: string;
  trigger_source_event_id?: string;
  war_room?: Record<string, unknown>;
  resolved_at?: string | null;
  closed_at?: string | null;
  // 边（按需 WithX 才出现）
  responders?: User[];
  events?: Event[];
};

// —— TimelineItem（ent/schema/timeline_action.go）——
export type TimelineType = Schemas["github_com_kevin_vigil_ent_timelineitem.Type"];

export interface TimelineActor {
  kind?: string; // system | user | integration | ai
  id?: string;
  name?: string;
}

export type TimelineItem = Required<
  Omit<Schemas["ent.TimelineItem"], "edges" | "detail" | "actor">
> & {
  actor: TimelineActor;
  detail?: Record<string, unknown>;
};

// —— 通用 list 响应（httputil.Paginated[T]）——
export interface ListResponse<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

// —— 引用实体（User 完整字段，登录态与管理页共用）——
export interface User {
  id: number;
  username?: string;
  name?: string;
  email?: string;
  phone?: string;
  status?: "active" | "disabled";
  timezone?: string;
  created_at?: string;
  updated_at?: string;
}

export type Event = Required<
  Omit<Schemas["ent.Event"], "edges">
>;

// —— Analytics（internal/analytics/engine.go，camelCase）——
export type AlertMetrics = Required<Schemas["internal_analytics.AlertMetrics"]>;

export type IncidentMetrics = Required<Schemas["internal_analytics.IncidentMetrics"]>;

export type TeamLoad = Required<Schemas["internal_analytics.TeamLoad"]>;

export type PostmortemMetrics = Required<Schemas["internal_analytics.PostmortemMetrics"]>;

export interface DashboardMetrics {
  alert: AlertMetrics;
  incident: IncidentMetrics;
  load: TeamLoad[];
  postmortem: PostmortemMetrics;
}

// —— Service（ent/schema/service.go，能力域 4/13）——
export type ServiceStatus = Schemas["github_com_kevin_vigil_ent_service.Status"];
export type Service = Required<
  Omit<Schemas["ent.Service"], "edges" | "description" | "labels">
> & {
  description?: string;
  labels?: Record<string, string>;
};

// —— Integration（ent/schema/service.go，能力域 1 接入点）——
export type IntegrationType = Schemas["github_com_kevin_vigil_ent_integration.Type"];
export type Integration = Required<
  Omit<Schemas["ent.Integration"], "edges">
>;
/** 创建接入点响应（含一次性 webhook 鉴权 token） */
export interface IntegrationCreated extends Integration {
  token: string;
}

// —— EscalationPolicy（ent/schema/escalation_policy.go，能力域 6）——
// target_id 为 string：后端存 schedule_id/user_id/team_id（schema.Target.target_id）。
export interface EscalationLevel {
  level: number;
  delay_minutes: number;
  targets: { type: string; target_id: string }[];
  notify_channels: string[];
}
export type EscalationPolicy = Required<
  Omit<Schemas["ent.EscalationPolicy"], "edges">
> & {
  levels?: EscalationLevel[];
};

// —— Team（能力域 13 团队管理）——
export interface Team {
  id: number;
  name: string;
  slug: string;
  description?: string;
  parent_team_id?: string;
  created_at?: string;
  updated_at?: string;
}

// —— Schedule（ent/schema/schedule.go，能力域 5）——
export type ScheduleType = Schemas["github_com_kevin_vigil_ent_schedule.Type"];
export interface ScheduleLayer {
  id: string;
  name: string;
  priority: number;
  rotation_id: string;
}
export type Schedule = Required<
  Omit<Schemas["ent.Schedule"], "edges" | "layers">
> & {
  layers?: ScheduleLayer[];
};

// 排班查询：某时刻在班人（internal_schedule.Oncall*）
export type OncallLayer = Required<Schemas["internal_schedule.OncallLayer"]>;
export type OncallResult = Required<Schemas["internal_schedule.OncallResult"]>;
// 预览：日期 → 在班人（internal_schedule.PreviewResult，days 为数组形态）
export type PreviewResult = Required<Schemas["internal_schedule.PreviewResult"]>;

// —— Runbook（ent/schema/runbook.go，能力域 9）——
export type RunbookType = Schemas["github_com_kevin_vigil_ent_runbook.Type"];
export type Runbook = Required<
  Omit<Schemas["ent.Runbook"], "edges" | "trigger" | "content_markdown" | "steps">
> & {
  trigger?: Record<string, unknown>;
  content_markdown?: string;
  steps?: unknown[];
};

// —— Postmortem（ent/schema/postmortem.go，能力域 12）——
export type PostmortemStatus = Schemas["github_com_kevin_vigil_ent_postmortem.Status"];

// ActionItem：后端实体用 description（非 title），status 枚举无 skipped。
export type ActionItemStatus = Schemas["github_com_kevin_vigil_ent_actionitem.Status"];
export type ActionItem = Required<
  Omit<Schemas["ent.ActionItem"], "edges" | "description" | "due_date" | "owner_id" | "tracker_url">
> & {
  description?: string;
  due_date?: string;
  owner_id?: string;
  tracker_url?: string;
};

export type Postmortem = Required<
  Omit<Schemas["ent.Postmortem"], "edges" | "sections" | "published_at" | "generated_by">
> & {
  // incident 是 edge（.WithIncident() 后形如 { id, ... }），无扁平 incident_id
  incident?: { id: number; [k: string]: unknown };
  sections?: Record<string, unknown>;
  published_at?: string;
  generated_by?: Schemas["github_com_kevin_vigil_ent_postmortem.GeneratedBy"];
  // 详情查询时带（edges.action_items）
  action_items?: ActionItem[];
};

// —— 通知规则（ent/schema/escalation_policy.go NotificationRule，能力域 7）——
export type NotificationRule = Required<
  Omit<Schemas["ent.NotificationRule"], "edges" | "condition" | "quiet_hours" | "template_id">
> & {
  condition?: Record<string, unknown>;
  quiet_hours?: Record<string, unknown>;
  template_id?: string;
};

// —— 抑制规则（ent/schema/suppression_rule.go，能力域 3 M3.2）——
export type SuppressionAction = Schemas["github_com_kevin_vigil_ent_suppressionrule.Action"];
export type SuppressionRule = Required<
  Omit<
    Schemas["ent.SuppressionRule"],
    "edges" | "match_labels" | "time_window" | "severity_filter" | "reduce_to" | "expires_at"
  >
> & {
  match_labels?: Record<string, string>;
  time_window?: Record<string, unknown>;
  severity_filter?: string[];
  reduce_to?: string;
  expires_at?: string | null;
};

// —— 通知模板（ent/schema/notification_template.go，能力域 7 M7.5）——
export type NotificationChannel = Schemas["github_com_kevin_vigil_ent_notificationtemplate.Channel"];
export type TemplateFormat = Schemas["github_com_kevin_vigil_ent_notificationtemplate.Format"];
export type NotificationTemplate = Required<
  Omit<Schemas["ent.NotificationTemplate"], "edges" | "actions">
> & {
  actions?: { type: string; label: string }[];
};

// —— RBAC（能力域 13）——
export type Role = Required<
  Omit<Schemas["ent.Role"], "edges" | "description">
> & {
  description?: string;
};

export type RoleBinding = Required<
  Omit<Schemas["ent.RoleBinding"], "edges" | "team_id" | "expires_at">
> & {
  // user/role 是 edge（WithUser/WithRole 后形如 { id, ... }），无扁平 user_id/role_id
  user?: { id: number; [k: string]: unknown };
  role?: { id: number; [k: string]: unknown };
  team_id?: string;
  expires_at?: string | null;
};

/** API Key（能力域 13 §API Key 管理）—— 列表视图不含明文 token */
export type APIKeyStatus = Schemas["github_com_kevin_vigil_ent_apikey.Status"];
export type APIKey = Required<
  Omit<Schemas["ent.APIKey"], "edges" | "scope" | "expires_at" | "last_used_at">
> & {
  scope?: string[];
  expires_at?: string | null;
  last_used_at?: string | null;
};

/** 创建 API Key 响应（含一次性明文 token，仅创建时返回；spec 未建模 token，单独叠加） */
export interface APIKeyCreated extends APIKey {
  token: string;
}

/** 审计日志条目（能力域 13 §审计日志）—— 只读 */
export type AuditLogResult = Schemas["github_com_kevin_vigil_ent_auditlog.Result"];
export type AuditLog = Required<
  Omit<Schemas["ent.AuditLog"], "edges" | "detail" | "resource_name" | "ip" | "user_agent">
> & {
  detail?: Record<string, unknown>;
  resource_name?: string;
  ip?: string;
  user_agent?: string;
};

/** 审计日志列表响应（含分页元数据） */
export interface AuditLogListResponse {
  items: AuditLog[];
  total: number;
  limit: number;
  offset: number;
}
