/**
 * badges.tsx —— 业务展示组件：severity/status 的 Badge 映射。
 * 标签走 i18n（enum.severity.* / enum.status.*），供多页复用。
 * 纯函数（formatTime/formatDuration）已拆到 format.ts，避免组件文件混导函数。
 */
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import type { IncidentStatus, Severity } from "@/lib/types";

/** severity → Badge variant（映射 index.css severity 色板）。 */
export function SeverityBadge({ severity }: { severity: Severity }) {
  const { t } = useTranslation();
  const variant =
    severity === "critical"
      ? "critical"
      : severity === "warning"
        ? "warning"
        : "info";
  return <Badge variant={variant}>{t(`enum.severity.${severity}`)}</Badge>;
}

/** status → Badge variant（映射 index.css status 色板）。 */
export function StatusBadge({ status }: { status: IncidentStatus }) {
  const { t } = useTranslation();
  return <Badge variant={status}>{t(`enum.status.${status}`)}</Badge>;
}
