import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Bell } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useIncidents } from "@/hooks/incidents";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, StatusBadge } from "@/lib/badges";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { IncidentStatus, Severity } from "@/lib/types";

const PAGE_SIZE = 20;

/**
 * Incidents —— 事件列表页。
 * 筛选（状态/严重度）+ 分页 + 点击进详情。对应后端 GET /incidents。
 */
export function Incidents() {
  const navigate = useNavigate();
  const { t } = useTranslation();
  // 筛选项标签走 i18n（common.all + enum.*），随语言切换。value 为后端枚举，不翻译。
  const statusFilters: { label: string; value: string }[] = [
    { label: t("common.all"), value: "" },
    { label: t("enum.status.triggered"), value: "triggered" },
    { label: t("enum.status.escalated"), value: "escalated" },
    { label: t("enum.status.acked"), value: "acked" },
    { label: t("enum.status.resolved"), value: "resolved" },
    { label: t("enum.status.closed"), value: "closed" },
  ];
  const severityFilters: { label: string; value: string }[] = [
    { label: t("common.all"), value: "" },
    { label: t("enum.severity.critical"), value: "critical" },
    { label: t("enum.severity.warning"), value: "warning" },
    { label: t("enum.severity.info"), value: "info" },
  ];
  const [status, setStatus] = useState("");
  const [severity, setSeverity] = useState("");
  const [page, setPage] = useState(0);

  const params = {
    status: status || undefined,
    severity: severity || undefined,
    limit: PAGE_SIZE,
    offset: page * PAGE_SIZE,
  };
  const { data, isLoading, isError } = useIncidents(params);

  // 切换筛选时回到第一页
  const onFilterChange = (setter: (v: string) => void) => (v: string) => {
    setter(v);
    setPage(0);
  };

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("incidents.title")}</h1>
        <p className="text-sm text-muted-foreground">
          {t("incidents.summary", { total })}
        </p>
      </div>

      {/* 筛选区 */}
      <div className="flex flex-wrap items-center gap-3">
        <FilterGroup
          label={t("incidents.filterStatus")}
          options={statusFilters}
          value={status}
          onChange={onFilterChange(setStatus)}
        />
        <FilterGroup
          label={t("incidents.filterSeverity")}
          options={severityFilters}
          value={severity}
          onChange={onFilterChange(setSeverity)}
        />
      </div>

      {/* 列表 */}
      <div className="overflow-hidden rounded-lg border bg-card">
        <table className="w-full text-sm">
          <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
            <tr>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colNumber")}</th>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colTitle")}</th>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colSeverity")}</th>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colStatus")}</th>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colEscalation")}</th>
              <th className="px-4 py-2.5 text-left font-medium">{t("incidents.colCreatedAt")}</th>
            </tr>
          </thead>
          <tbody>
            {isLoading ? (
              Array.from({ length: 5 }).map((_, i) => (
                <tr key={i} className="border-b last:border-0">
                  {Array.from({ length: 6 }).map((__, j) => (
                    <td key={j} className="px-4 py-3">
                      <Skeleton className="h-5 w-full" />
                    </td>
                  ))}
                </tr>
              ))
            ) : isError ? null : items.length === 0 ? (
              <tr>
                <td colSpan={6} className="p-0">
                  <EmptyState
                    icon={<Bell className="h-8 w-8" />}
                    title={t("incidents.empty")}
                    description={t("incidents.emptyHint")}
                  />
                </td>
              </tr>
            ) : (
              items.map((inc) => (
                <tr
                  key={inc.id}
                  onClick={() => navigate(`/incidents/${inc.id}`)}
                  className="cursor-pointer border-b transition-colors last:border-0 hover:bg-muted/40"
                >
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
                    {inc.number}
                  </td>
                  <td className="px-4 py-3 font-medium">{inc.title}</td>
                  <td className="px-4 py-3">
                    <SeverityBadge severity={inc.severity} />
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={inc.status} />
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    L{inc.current_level}
                    {inc.escalated_count > 0 &&
                      ` · ${t("incidents.escalatedTimes", { count: inc.escalated_count })}`}
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {formatTime(inc.created_at)}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* 分页 */}
      {total > PAGE_SIZE && (
        <div className="flex items-center justify-between text-sm">
          <span className="text-muted-foreground">
            {t("common.pageInfo", { page: page + 1, total: totalPages })}
          </span>
          <div className="flex gap-2">
            <button
              className="rounded-md border px-3 py-1.5 disabled:opacity-50"
              disabled={page === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              {t("common.prev")}
            </button>
            <button
              className="rounded-md border px-3 py-1.5 disabled:opacity-50"
              disabled={page + 1 >= totalPages}
              onClick={() => setPage((p) => p + 1)}
            >
              {t("common.next")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

/** FilterGroup 一组单选筛选 chip。 */
function FilterGroup({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { label: string; value: string }[];
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      {options.map((opt) => (
        <button
          key={opt.value}
          onClick={() => onChange(opt.value)}
          className={cn(
            "rounded-md px-2.5 py-1 text-xs transition-colors",
            value === opt.value
              ? "bg-primary text-primary-foreground"
              : "bg-muted text-muted-foreground hover:bg-accent",
          )}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}

// 保持 Severity 类型被引用（供未来扩展筛选类型安全）。
export type { IncidentStatus, Severity };
