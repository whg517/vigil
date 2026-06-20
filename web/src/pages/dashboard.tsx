import { useNavigate } from "react-router-dom";
import { AlertTriangle, Bell, Clock, Activity } from "lucide-react";
import { useDashboard } from "@/hooks/incidents";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, formatDuration } from "@/lib/format";

/**
 * Dashboard —— 仪表盘，接后端 GET /analytics/dashboard。
 * 4 个 KPI + 事件严重度分布 + 团队负载。
 * 注：后端 analytics 部分 metric（如 MTTA）当前为 0（已知简化），值 0 显示 "—"。
 */
export function Dashboard() {
  const navigate = useNavigate();
  const { data, isLoading, isError } = useDashboard(7);

  const alert = data?.Alert;
  const inc = data?.Incident;
  const load = data?.Load ?? [];

  // 活跃事件 = 非已解决/已关闭
  const activeCount = inc
    ? (inc.ByStatus["triggered"] ?? 0) +
      (inc.ByStatus["escalated"] ?? 0) +
      (inc.ByStatus["acked"] ?? 0)
    : 0;

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">仪表盘</h1>
          <p className="text-sm text-muted-foreground">近 7 天概览</p>
        </div>
        <Button onClick={() => navigate("/incidents")}>查看事件</Button>
      </div>

      {/* KPI 卡片 */}
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        <KpiCard
          icon={<Bell className="h-4 w-4" />}
          label="活跃事件"
          value={isLoading ? null : activeCount}
        />
        <KpiCard
          icon={<AlertTriangle className="h-4 w-4" />}
          label="近 7 天告警"
          value={isLoading ? null : alert?.Total}
          sub={
            alert && alert.Total > 0
              ? `降噪率 ${Math.round(alert.NoiseRate * 100)}%`
              : undefined
          }
        />
        <KpiCard
          icon={<Clock className="h-4 w-4" />}
          label="MTTA 平均确认"
          value={isLoading ? null : inc?.MTTARatio}
          renderValue={(v) => formatDuration(v as number)}
        />
        <KpiCard
          icon={<Clock className="h-4 w-4" />}
          label="MTTR 平均解决"
          value={isLoading ? null : inc?.MTTRatio}
          renderValue={(v) => formatDuration(v as number)}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* 事件严重度分布 */}
        <Card>
          <CardHeader>
            <CardTitle>事件严重度分布（近 7 天）</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <Skeleton className="h-20 w-full" />
            ) : isError ? null : !inc || inc.Total === 0 ? (
              <EmptyState title="暂无事件数据" />
            ) : (
              <SeverityDistribution bySeverity={inc.BySeverity} total={inc.Total} />
            )}
          </CardContent>
        </Card>

        {/* 团队负载 */}
        <Card>
          <CardHeader>
            <CardTitle>团队负载（事件数）</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <Skeleton className="h-20 w-full" />
            ) : load.length === 0 ? (
              <EmptyState
                icon={<Activity className="h-8 w-8" />}
                title="暂无团队数据"
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
  const rows: { key: "critical" | "warning" | "info"; label: string }[] = [
    { key: "critical", label: "严重" },
    { key: "warning", label: "警告" },
    { key: "info", label: "信息" },
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
  load: { TeamID: number; TeamName: string; Incidents: number }[];
}) {
  const max = Math.max(1, ...load.map((t) => t.Incidents));
  return (
    <div className="space-y-2.5">
      {load.map((t) => (
        <div key={t.TeamID} className="space-y-1">
          <div className="flex items-center justify-between text-sm">
            <span>{t.TeamName || `团队 ${t.TeamID}`}</span>
            <span className="text-muted-foreground">{t.Incidents}</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full bg-primary"
              style={{ width: `${(t.Incidents / max) * 100}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}
