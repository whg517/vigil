import { useNavigate } from "react-router-dom";
import { AlertTriangle, Bell, Clock, Activity, Radio } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useDashboard } from "@/hooks/incidents";
import { useDashboardWS } from "@/hooks/use-dashboard-ws";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge } from "@/lib/badges";
import { formatDuration } from "@/lib/format";

/**
 * Dashboard —— 仪表盘，接后端 GET /analytics/dashboard。
 * 4 个 KPI + 事件严重度分布 + 团队负载。
 * 注：后端 analytics 部分 metric（如 MTTA）当前为 0（已知简化），值 0 显示 "—"。
 */
export function Dashboard() {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const { data, isLoading, isError } = useDashboard(7);
  // 实时化（P4·B3）：订阅 /ws/dashboard，任一 incident 变更即 invalidate 重拉 KPI，免轮询。
  // 无 analytics.view 时握手被拒、退避重试，仪表盘仍靠常规拉取兜底（不白屏）。
  useDashboardWS();

  const alert = data?.alert;
  const inc = data?.incident;
  const load = data?.load ?? [];

  // 活跃事件 = 非已解决/已关闭
  const activeCount = inc
    ? (inc.byStatus["triggered"] ?? 0) +
      (inc.byStatus["escalated"] ?? 0) +
      (inc.byStatus["acked"] ?? 0)
    : 0;

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("dashboard.title")}</h1>
          <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
            <Radio className="h-3.5 w-3.5 text-severity-info" />
            {t("dashboard.overview")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => navigate("/wall")}>
            {t("dashboard.wall")}
          </Button>
          <Button onClick={() => navigate("/incidents")}>{t("dashboard.viewIncidents")}</Button>
        </div>
      </div>

      {/* KPI 卡片 */}
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        <KpiCard
          icon={<Bell className="h-4 w-4" />}
          label={t("dashboard.kpiActive")}
          value={isLoading ? null : activeCount}
        />
        <KpiCard
          icon={<AlertTriangle className="h-4 w-4" />}
          label={t("dashboard.kpiAlerts7d")}
          value={isLoading ? null : alert?.total}
          sub={
            alert && alert.total > 0
              ? t("dashboard.noiseRate", { rate: Math.round(alert.noiseRate * 100) })
              : undefined
          }
        />
        <KpiCard
          icon={<Clock className="h-4 w-4" />}
          label={t("dashboard.kpiMtta")}
          value={isLoading ? null : inc?.mttaratio}
          renderValue={(v) => formatDuration(v as number)}
        />
        <KpiCard
          icon={<Clock className="h-4 w-4" />}
          label={t("dashboard.kpiMttr")}
          value={isLoading ? null : inc?.mttratio}
          renderValue={(v) => formatDuration(v as number)}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* 事件严重度分布 */}
        <Card>
          <CardHeader>
            <CardTitle>{t("dashboard.severityDist")}</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <Skeleton className="h-20 w-full" />
            ) : isError ? null : !inc || inc.total === 0 ? (
              <EmptyState title={t("dashboard.noIncidentData")} />
            ) : (
              <SeverityDistribution bySeverity={inc.bySeverity} total={inc.total} />
            )}
          </CardContent>
        </Card>

        {/* 团队负载 */}
        <Card>
          <CardHeader>
            <CardTitle>{t("dashboard.teamLoad")}</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <Skeleton className="h-20 w-full" />
            ) : load.length === 0 ? (
              <EmptyState
                icon={<Activity className="h-8 w-8" />}
                title={t("dashboard.noTeamData")}
              />
            ) : (
              <TeamLoadBars load={load} />
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

/** KpiCard 单个指标卡。value=null 显示骨架，0 显示 "—"。 */
function KpiCard({
  icon,
  label,
  value,
  sub,
  renderValue,
}: {
  icon: React.ReactNode;
  label: string;
  value: number | null | undefined;
  sub?: string;
  renderValue?: (v: unknown) => React.ReactNode;
}) {
  const display =
    value == null
      ? null
      : value === 0
        ? "—"
        : renderValue
          ? renderValue(value)
          : value;
  return (
    <Card>
      <CardContent className="flex flex-col gap-2 p-4">
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          {icon}
          {label}
        </div>
        {display === null ? (
          <Skeleton className="h-7 w-16" />
        ) : (
          <div className="text-2xl font-semibold">{display}</div>
        )}
        {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
      </CardContent>
    </Card>
  );
}

function SeverityDistribution({
  bySeverity,
  total,
}: {
  bySeverity: Record<string, number>;
  total: number;
}) {
  // 严重度标签由 SeverityBadge 内部 i18n 渲染，这里仅需 key 与色板。
  const rows: { key: "critical" | "warning" | "info" }[] = [
    { key: "critical" },
    { key: "warning" },
    { key: "info" },
  ];
  return (
    <div className="space-y-3">
      {rows.map((r) => {
        const count = bySeverity[r.key] ?? 0;
        const pct = total > 0 ? Math.round((count / total) * 100) : 0;
        return (
          <div key={r.key} className="space-y-1">
            <div className="flex items-center justify-between text-sm">
              <SeverityBadge severity={r.key} />
              <span className="text-muted-foreground">
                {count} · {pct}%
              </span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className={
                  r.key === "critical"
                    ? "h-full bg-severity-critical"
                    : r.key === "warning"
                      ? "h-full bg-severity-warning"
                      : "h-full bg-severity-info"
                }
                style={{ width: `${pct}%` }}
              />
            </div>
          </div>
        );
      })}
    </div>
  );
}

function TeamLoadBars({
  load,
}: {
  load: { teamID: number; teamName: string; incidents: number }[];
}) {
  const { t } = useTranslation();
  const max = Math.max(1, ...load.map((item) => item.incidents));
  return (
    <div className="space-y-2.5">
      {load.map((item) => (
        <div key={item.teamID} className="space-y-1">
          <div className="flex items-center justify-between text-sm">
            <span>{item.teamName || t("dashboard.teamFallback", { id: item.teamID })}</span>
            <span className="text-muted-foreground">{item.incidents}</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full bg-primary"
              style={{ width: `${(item.incidents / max) * 100}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}
