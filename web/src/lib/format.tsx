/**
 * format —— 业务展示工具：severity/status 的 Badge 映射、时间格式化。
 * 列表页与详情页共用，避免各页重复 if/else。
 */
import { Badge } from "@/components/ui/badge";
import type { IncidentStatus, Severity } from "@/lib/types";

/** severity → Badge variant（映射 index.css severity 色板）。 */
export function SeverityBadge({ severity }: { severity: Severity }) {
  const variant =
    severity === "critical"
      ? "critical"
      : severity === "warning"
        ? "warning"
        : "info";
  const label =
    severity === "critical"
      ? "严重"
      : severity === "warning"
        ? "警告"
        : "信息";
  return <Badge variant={variant}>{label}</Badge>;
}

/** status → Badge variant（映射 index.css status 色板）。 */
const statusLabel: Record<IncidentStatus, string> = {
  triggered: "待响应",
  escalated: "已升级",
  acked: "已确认",
  resolved: "已解决",
  closed: "已关闭",
};

export function StatusBadge({ status }: { status: IncidentStatus }) {
  return <Badge variant={status}>{statusLabel[status]}</Badge>;
}

/**
 * formatTime 把 ISO 时间串格式化为本地可读形式。
 * 无值（解析失败）时返回 "—"。
 */
export function formatTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  // YYYY-MM-DD HH:mm
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** formatDuration 把秒数格式化为 "X分Y秒"/"X时Y分" 等可读时长。 */
export function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)}秒`;
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m}分`;
  const h = Math.floor(m / 60);
  return `${h}时${m % 60}分`;
}
