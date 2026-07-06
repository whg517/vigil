/**
 * Wall —— 值班大屏（NOC 挂墙只读视图，P4·B3）。
 *
 * 定位：机房/NOC 大屏常驻展示，非交互（只读）。相较仪表盘：
 *   · 全屏深色、大字号、高对比度，远距离可读；
 *   · 活跃事件滚动列表，critical 红色醒目、闪烁高亮；
 *   · 关键 KPI（活跃事件 / 近 N 分钟告警 / MTTA / MTTR）大数字；
 *   · 当前值班人（复用 oncall 排班）；
 *   · WS 实时刷新（/ws/dashboard，免轮询）+ 常规拉取兜底；
 *   · 无侧边栏（独立路由 /wall，不套 AppShell）。
 *
 * 数据全部复用现有 analytics / incidents / oncall 端点 + WS 增量，不新增后端。
 */
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { AlertTriangle, Radio, Clock, Bell, Users, X } from "lucide-react";
import { useDashboard, useIncidents } from "@/hooks/incidents";
import { useSchedules, useOncall } from "@/hooks/oncall";
import { useDashboardWS } from "@/hooks/use-dashboard-ws";
import { formatDuration } from "@/lib/format";
import type { Incident, Severity } from "@/lib/types";

/** 活跃 = 未解决/未关闭。 */
const ACTIVE_STATUSES = new Set(["triggered", "escalated", "acked"]);

export function Wall() {
  const navigate = useNavigate();
  const [live, setLive] = useState(true);
  const [now, setNow] = useState(() => new Date());

  // 实时化：WS 增量到达即 invalidate 重拉；同时用 live 指示灯反映连接状态。
  useDashboardWS(() => setLive(true));

  const { data: dash } = useDashboard(7);
  // 活跃事件：拉未解决的（triggered/escalated/acked），按创建时间倒序取前若干。
  const { data: incData } = useIncidents({ limit: 50 });

  // 大屏时钟（每秒走针）。
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(t);
  }, []);

  const inc = dash?.incident;
  const alert = dash?.alert;
  const activeIncidents = (incData?.items ?? []).filter((i) =>
    ACTIVE_STATUSES.has(i.status),
  );
  const activeCount = activeIncidents.length;
  const criticalCount = activeIncidents.filter((i) => i.severity === "critical").length;

  return (
    <div className="flex min-h-screen flex-col bg-zinc-950 text-zinc-100">
      {/* 顶栏：标题 + 时钟 + 实时指示 + 退出 */}
      <header className="flex items-center justify-between border-b border-zinc-800 px-8 py-5">
        <div className="flex items-center gap-3">
          <h1 className="text-3xl font-bold tracking-wide">Vigil 值班大屏</h1>
          <span
            className={`inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-sm font-medium ${
              live ? "bg-emerald-500/15 text-emerald-400" : "bg-zinc-700/40 text-zinc-400"
            }`}
          >
            <Radio className={`h-4 w-4 ${live ? "animate-pulse" : ""}`} />
            {live ? "实时" : "连接中"}
          </span>
        </div>
        <div className="flex items-center gap-6">
          <div className="font-mono text-3xl tabular-nums tracking-widest text-zinc-200">
            {now.toLocaleTimeString("zh-CN", { hour12: false })}
          </div>
          <button
            onClick={() => navigate("/")}
            className="rounded-md border border-zinc-700 p-2 text-zinc-400 transition hover:bg-zinc-800 hover:text-zinc-100"
            aria-label="退出大屏"
            title="退出大屏"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
      </header>

      {/* KPI 大数字条 */}
      <div className="grid grid-cols-2 gap-4 px-8 py-6 md:grid-cols-4">
        <WallKpi
          icon={<Bell className="h-6 w-6" />}
          label="活跃事件"
          value={activeCount}
          danger={criticalCount > 0}
          hint={criticalCount > 0 ? `${criticalCount} 个严重` : undefined}
        />
        <WallKpi
          icon={<AlertTriangle className="h-6 w-6" />}
          label="近 7 天告警"
          value={alert?.total ?? 0}
          hint={alert && alert.total > 0 ? `降噪 ${Math.round(alert.noiseRate * 100)}%` : undefined}
        />
        <WallKpi
          icon={<Clock className="h-6 w-6" />}
          label="MTTA 平均确认"
          value={formatDuration(inc?.mttaratio)}
        />
        <WallKpi
          icon={<Clock className="h-6 w-6" />}
          label="MTTR 平均解决"
          value={formatDuration(inc?.mttratio)}
        />
      </div>

      {/* 主体：活跃事件（左，主视觉）+ 值班人（右） */}
      <div className="grid flex-1 grid-cols-1 gap-4 px-8 pb-8 lg:grid-cols-3">
        <section className="lg:col-span-2 flex flex-col rounded-xl border border-zinc-800 bg-zinc-900/60">
          <div className="flex items-center justify-between border-b border-zinc-800 px-6 py-4">
            <h2 className="text-xl font-semibold">活跃事件</h2>
            <span className="text-sm text-zinc-400">{activeCount} 个待处置</span>
          </div>
          <div className="flex-1 overflow-y-auto">
            {activeCount === 0 ? (
              <div className="flex h-full min-h-[200px] items-center justify-center text-2xl font-medium text-emerald-400">
                全部平稳，无活跃事件
              </div>
            ) : (
              <ul className="divide-y divide-zinc-800">
                {activeIncidents.map((i) => (
                  <ActiveRow key={i.id} incident={i} />
                ))}
              </ul>
            )}
          </div>
        </section>

        <aside className="flex flex-col rounded-xl border border-zinc-800 bg-zinc-900/60">
          <div className="flex items-center gap-2 border-b border-zinc-800 px-6 py-4">
            <Users className="h-5 w-5 text-zinc-400" />
            <h2 className="text-xl font-semibold">当前值班</h2>
          </div>
          <div className="flex-1 overflow-y-auto px-6 py-4">
            <OncallPanel />
          </div>
        </aside>
      </div>
    </div>
  );
}

/** WallKpi 大屏 KPI 大数字卡。 */
function WallKpi({
  icon,
  label,
  value,
  hint,
  danger,
}: {
  icon: React.ReactNode;
  label: string;
  value: number | string;
  hint?: string;
  danger?: boolean;
}) {
  return (
    <div
      className={`rounded-xl border p-5 ${
        danger ? "border-red-500/50 bg-red-500/10" : "border-zinc-800 bg-zinc-900/60"
      }`}
    >
      <div className="flex items-center gap-2 text-sm font-medium text-zinc-400">
        {icon}
        {label}
      </div>
      <div
        className={`mt-2 text-5xl font-bold tabular-nums ${danger ? "text-red-400" : "text-zinc-50"}`}
      >
        {value === 0 || value === "—" ? (value === 0 ? "0" : "—") : value}
      </div>
      {hint && <div className="mt-1 text-sm text-zinc-400">{hint}</div>}
    </div>
  );
}

/** ActiveRow 活跃事件行。critical 红色醒目 + 左侧色条闪烁。 */
function ActiveRow({ incident }: { incident: Incident }) {
  const isCritical = incident.severity === "critical";
  return (
    <li
      className={`flex items-center gap-4 px-6 py-4 ${
        isCritical ? "bg-red-500/10" : ""
      }`}
    >
      <span
        className={`h-10 w-1.5 rounded-full ${severityBar(incident.severity)} ${
          isCritical ? "animate-pulse" : ""
        }`}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="font-mono text-sm text-zinc-400">{incident.number}</span>
          <SeverityTag severity={incident.severity} />
          <StatusTag status={incident.status} />
        </div>
        <div className="mt-0.5 truncate text-lg font-medium text-zinc-100">
          {incident.title}
        </div>
      </div>
    </li>
  );
}

function severityBar(sev: Severity): string {
  if (sev === "critical") return "bg-red-500";
  if (sev === "warning") return "bg-amber-500";
  return "bg-sky-500";
}

function SeverityTag({ severity }: { severity: Severity }) {
  const label = severity === "critical" ? "严重" : severity === "warning" ? "警告" : "信息";
  const cls =
    severity === "critical"
      ? "bg-red-500/20 text-red-300"
      : severity === "warning"
        ? "bg-amber-500/20 text-amber-300"
        : "bg-sky-500/20 text-sky-300";
  return (
    <span className={`rounded px-2 py-0.5 text-xs font-semibold ${cls}`}>{label}</span>
  );
}

function StatusTag({ status }: { status: string }) {
  const map: Record<string, string> = {
    triggered: "已触发",
    escalated: "已升级",
    acked: "已确认",
  };
  return (
    <span className="rounded bg-zinc-700/50 px-2 py-0.5 text-xs text-zinc-300">
      {map[status] ?? status}
    </span>
  );
}

/**
 * OncallPanel 当前值班人面板。列出所有排班的当前在班人（复用 oncall 端点）。
 * 无排班/未配置时给出提示，不报错。
 */
function OncallPanel() {
  const { data: schedules } = useSchedules();
  const list = schedules ?? [];
  if (list.length === 0) {
    return <div className="text-zinc-500">暂无排班</div>;
  }
  return (
    <div className="space-y-4">
      {list.map((s) => (
        <OncallRow key={s.id} scheduleId={s.id} scheduleName={s.name} />
      ))}
    </div>
  );
}

function OncallRow({ scheduleId, scheduleName }: { scheduleId: number; scheduleName: string }) {
  const { data } = useOncall(scheduleId);
  // OncallResult 按层组织（primary/secondary/override）；大屏扁平化为「当前在班人」集合。
  const users = (data?.layers ?? []).flatMap((l) => l.users ?? []);
  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-950/40 px-4 py-3">
      <div className="text-sm text-zinc-400">{scheduleName}</div>
      {users.length === 0 ? (
        <div className="mt-1 text-lg font-medium text-amber-400">空班（无人值班）</div>
      ) : (
        <div className="mt-1 flex flex-wrap gap-2">
          {users.map((u) => (
            <span
              key={u.id}
              className="rounded-full bg-emerald-500/15 px-3 py-1 text-lg font-semibold text-emerald-300"
            >
              {u.name || u.username || `#${u.id}`}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
