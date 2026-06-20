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
