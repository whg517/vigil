/**
 * 类型定义 —— 从 OpenAPI spec（src/lib/api/types.gen.ts）派生的薄别名层。
 *
 * 权威源是后端 handler 注解经 swag 重新生成的 OpenAPI 3.1 spec；
 * 本文件只做两件事：
 *   1. 给丑陋的生成 schema 名（ent.Incident、analytics.AlertMetrics…）起易用别名；
 *   2. 用 Required<> 把 ent 实体的可选字段标为必填（ent JSON 总会输出这些字段，
 *      swag 无法推断 required-ness，故生成端一律 optional，这里收紧）。
 *
 * 不要在此手写字段；如需新增/修改字段，改后端注解后 `pnpm gen:types` 重生成。
 */
import type { components } from "@/lib/api/types.gen";

type Schemas = components["schemas"];

// —— Incident（ent/schema/incident.go）——
export type Severity = Schemas["incident.Severity"];
// 状态机：triggered → escalated → acked → resolved → closed
export type IncidentStatus = Schemas["incident.Status"];
export type Priority = Schemas["incident.Priority"];

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
export type TimelineType = Schemas["timelineitem.Type"];

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
// ENG-02：User 从 Schemas 派生（原手写易与后端 ent.User drift）。
// status 用 Schemas 的 enum 引用（与 ent_user.Status 一致："active"|"disabled"）。
// must_change_password 放开（T0.4）：驱动首登强制改密重定向（RequireAuth / 登录后跳转）。
// ent JSON 总会输出该布尔字段，故并入 Required<> 收紧为必填。
export type User = Required<
  Omit<Schemas["ent.User"], "edges" | "im_accounts">
>;

export type Event = Required<
  Omit<Schemas["ent.Event"], "edges">
>;

// —— Analytics（internal/analytics/engine.go，camelCase）——
export type AlertMetrics = Required<Schemas["analytics.AlertMetrics"]>;

export type IncidentMetrics = Required<Schemas["analytics.IncidentMetrics"]>;

export type TeamLoad = Required<Schemas["analytics.TeamLoad"]>;

export type PostmortemMetrics = Required<Schemas["analytics.PostmortemMetrics"]>;

export interface DashboardMetrics {
  alert: AlertMetrics;
  incident: IncidentMetrics;
  load: TeamLoad[];
  postmortem: PostmortemMetrics;
}

// —— Service（ent/schema/service.go，能力域 4/13）——
export type ServiceStatus = Schemas["service.Status"];
export type Service = Required<
  Omit<Schemas["ent.Service"], "edges" | "description" | "labels">
> & {
  description?: string;
  labels?: Record<string, string>;
};

// —— Integration（ent/schema/service.go，能力域 1 接入点）——
export type IntegrationType = Schemas["integration.Type"];
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

// ENG-02：Team 从 Schemas 派生（原手写易与后端 ent.Team drift）。
export type Team = Required<Omit<Schemas["ent.Team"], "edges">>;

// —— Schedule（ent/schema/schedule.go，能力域 5）——
export type ScheduleType = Schemas["schedule.Type"];
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

// 排班查询：某时刻在班人（schedule.Oncall*）
export type OncallLayer = Required<Schemas["schedule.OncallLayer"]>;
export type OncallResult = Required<Schemas["schedule.OncallResult"]>;
// 预览：日期 → 在班人（schedule.PreviewResult，days 为数组形态）
export type PreviewResult = Required<Schemas["schedule.PreviewResult"]>;

// —— Runbook（ent/schema/runbook.go，能力域 9）——
export type RunbookType = Schemas["runbook.Type"];
export type Runbook = Required<
  Omit<Schemas["ent.Runbook"], "edges" | "trigger" | "content_markdown" | "steps">
> & {
  trigger?: Record<string, unknown>;
  content_markdown?: string;
  steps?: unknown[];
};

// 执行结果（POST /runbooks/:id/execute 返回体，runbook.ExecuteResult）。
// 后端补 snake_case json tag 后前端才能逐步读到成败/输出与"写步骤被阻断待审批"（audit B20）。
export type RunbookStepResult = Schemas["runbook.StepResult"];
export type RunbookExecuteResult = Schemas["runbook.ExecuteResult"];

// —— Postmortem（ent/schema/postmortem.go，能力域 12）——
export type PostmortemStatus = Schemas["postmortem.Status"];

// ActionItem：后端实体用 description（非 title），status 枚举无 skipped。
export type ActionItemStatus = Schemas["actionitem.Status"];
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
  generated_by?: Schemas["postmortem.GeneratedBy"];
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
export type SuppressionAction = Schemas["suppressionrule.Action"];
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
export type NotificationChannel = Schemas["notificationtemplate.Channel"];
export type TemplateFormat = Schemas["notificationtemplate.Format"];
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
export type APIKeyStatus = Schemas["apikey.Status"];
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
export type AuditLogResult = Schemas["auditlog.Result"];
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

// —— Credential 凭据托管（ent/schema/credential.go，能力域 6 Runbook/工单执行器凭据）——
// 密文经 Sensitive 恒不回显（list/get 只返元数据），故派生类型无 secret 字段。
export type CredentialType = Schemas["credential.Type"];
export type Credential = Required<
  Omit<Schemas["ent.Credential"], "edges" | "config">
> & {
  config?: Record<string, unknown>;
  // team 是 edge（WithTeam 后形如 { id, name, ... }）；后端 list 未 eager-load，通常缺省。
  team?: { id: number; [k: string]: unknown };
};

// —— Subscription 个人订阅（ent/schema/subscription.go，能力域 4/7 T4.4）——
// 当前登录用户的自助订阅：scope 二选一（team 或 service edge）+ min_severity + channels。
export type SubscriptionSeverity = Schemas["subscription.MinSeverity"];
export type Subscription = Required<
  Omit<Schemas["ent.Subscription"], "edges" | "channels">
> & {
  channels?: string[];
  // team/service 是 edge（list 时 WithTeam/WithService 回带），形如 { id, name, ... }。
  team?: { id: number; [k: string]: unknown };
  service?: { id: number; [k: string]: unknown };
};

// —— TicketIntegration 工单集成（ent/schema/ticket_integration.go，能力域 4 T4.3）——
// 凭据经 Sensitive 恒不回显（list/get 不返 credential/callback_secret），故派生类型无这两个字段。
export type TicketIntegrationType = Schemas["ticketintegration.Type"];
export type TicketIntegration = Required<
  Omit<Schemas["ent.TicketIntegration"], "edges" | "config">
> & {
  config?: Record<string, unknown>;
  team?: { id: number; [k: string]: unknown };
};

// —— AI 诊断（能力域 11）——
// DiagnoseResult 字段为 snake_case json tag，与后端 ai.DiagnoseResult 一致。
export type DiagnoseResult = Required<
  Schemas["ai.DiagnoseResult"]
> & {
  // evidence 是数组，Required 会保留其 optional 性，这里显式标注。
  evidence?: Record<string, unknown>[];
};

/** AI 诊断未启用时后端返回的降级响应（200，{status:"disabled"}）。 */
export interface DiagnoseDisabled {
  status: "disabled";
  message: string;
}

// AIInsightStatus human-in-the-loop 生命周期：suggested→accepted→applied（或 rejected）。
export type AIInsightStatus = Schemas["aiinsight.Status"];
export type AIInsightType = Schemas["aiinsight.Type"];
export type AIInsightStage = Schemas["aiinsight.Stage"];

// AIInsight AI 洞察（T3.1 可读持久化）。诊断产出落库，前端加载历史列表持久呈现，
// accept/reject 后状态持久（不再刷新即丢）。
export type AIInsight = Required<
  Omit<Schemas["ent.AIInsight"], "edges" | "content" | "evidence">
> & {
  content?: Record<string, unknown>;
  evidence?: Record<string, unknown>[];
};
