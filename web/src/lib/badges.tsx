/**
 * format.tsx —— 业务展示组件：severity/status 的 Badge 映射。
 * 纯函数（formatTime/formatDuration）已拆到 format.ts，避免组件文件混导函数。
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
