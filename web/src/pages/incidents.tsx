import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Bell } from "lucide-react";
import { useIncidents } from "@/hooks/incidents";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, StatusBadge, formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { IncidentStatus, Severity } from "@/lib/types";

const PAGE_SIZE = 20;

const statusFilters: { label: string; value: string }[] = [
  { label: "全部", value: "" },
  { label: "待响应", value: "triggered" },
  { label: "已升级", value: "escalated" },
  { label: "已确认", value: "acked" },
  { label: "已解决", value: "resolved" },
  { label: "已关闭", value: "closed" },
];

const severityFilters: { label: string; value: string }[] = [
  { label: "全部", value: "" },
  { label: "严重", value: "critical" },
  { label: "警告", value: "warning" },
  { label: "信息", value: "info" },
];

/**
 * Incidents —— 事件列表页。
 * 筛选（状态/严重度）+ 分页 + 点击进详情。对应后端 GET /incidents。
 */
export function Incidents() {
  const navigate = useNavigate();
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
        <h1 className="text-2xl font-semibold tracking-tight">事件</h1>
        <p className="text-sm text-muted-foreground">
          共 {total} 条 · 按状态与严重度筛选
        </p>
      </div>

      {/* 筛选区 */}
      <div className="flex flex-wrap items-center gap-3">
        <FilterGroup
          label="状态"
          options={statusFilters}
          value={status}
          onChange={onFilterChange(setStatus)}
        />
        <FilterGroup
          label="严重度"
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
              <th className="px-4 py-2.5 text-left font-medium">编号</th>
              <th className="px-4 py-2.5 text-left font-medium">标题</th>
              <th className="px-4 py-2.5 text-left font-medium">严重度</th>
              <th className="px-4 py-2.5 text-left font-medium">状态</th>
              <th className="px-4 py-2.5 text-left font-medium">升级</th>
              <th className="px-4 py-2.5 text-left font-medium">创建时间</th>
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
                    title="暂无事件"
                    description="当前筛选条件下没有事件"
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
                    {inc.escalated_count > 0 && ` · ${inc.escalated_count}次`}
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
            第 {page + 1} / {totalPages} 页
          </span>
          <div className="flex gap-2">
            <button
              className="rounded-md border px-3 py-1.5 disabled:opacity-50"
              disabled={page === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              上一页
            </button>
            <button
              className="rounded-md border px-3 py-1.5 disabled:opacity-50"
              disabled={page + 1 >= totalPages}
              onClick={() => setPage((p) => p + 1)}
            >
              下一页
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
