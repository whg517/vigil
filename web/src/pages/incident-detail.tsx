import { useParams, useNavigate } from "react-router-dom";
import { ArrowLeft, Check, ChevronUp, Hand } from "lucide-react";
import { useIncident, useIncidentAction, useTimeline } from "@/hooks/incidents";
import { useIncidentWS } from "@/hooks/use-incident-ws";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, StatusBadge } from "@/lib/badges";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { TimelineType } from "@/lib/types";

/**
 * IncidentDetail —— 事件详情页。
 * 头部信息 + 操作（确认/解决/升级）+ 时间线。
 * 对应后端 GET /incidents/:id、GET /incidents/:id/timeline、POST .../ack|resolve|escalate。
 */
export function IncidentDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
	const incId = Number(id);

	// WebSocket 实时同步（能力域 8）：incident 变更自动刷新，无需手动刷新页面
	useIncidentWS(incId);

	const { data: inc, isLoading } = useIncident(incId);
  const { data: tl } = useTimeline(incId);
  const action = useIncidentAction(incId);

  if (isLoading) return <DetailSkeleton />;
  if (!inc) {
    return (
      <div className="p-6">
        <BackButton onClick={() => navigate("/incidents")} />
        <EmptyState
          title="事件不存在"
          description={`未找到 ID 为 ${id} 的事件`}
        />
      </div>
    );
  }

  const isClosed =
    inc.status === "resolved" || inc.status === "closed";
  const isAcked = inc.status === "acked";

  const items = tl?.items ?? [];

  return (
    <div className="space-y-4 p-6">
      <BackButton onClick={() => navigate("/incidents")} />

      {/* 头部信息 */}
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {inc.number}
            </span>
            <SeverityBadge severity={inc.severity} />
            <StatusBadge status={inc.status} />
          </div>
          <h1 className="text-xl font-semibold">{inc.title}</h1>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <span>优先级 {inc.priority.toUpperCase()}</span>
            <span>当前升级层级 L{inc.current_level}</span>
            <span>累计升级 {inc.escalated_count} 次</span>
            <span>创建于 {formatTime(inc.created_at)}</span>
            {inc.resolved_at && (
              <span>解决于 {formatTime(inc.resolved_at)}</span>
            )}
          </div>
          {inc.summary && (
            <p className="max-w-2xl pt-1 text-sm text-muted-foreground">
              {inc.summary}
            </p>
          )}
        </div>

        {/* 操作区：按当前状态启用/禁用 */}
        <div className="flex shrink-0 gap-2">
          <Button
            variant="default"
            size="sm"
            disabled={isAcked || isClosed || action.isPending}
            onClick={() => action.mutate("ack")}
          >
            <Hand className="h-4 w-4" /> 确认
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={isClosed || action.isPending}
            onClick={() => action.mutate("escalate")}
          >
            <ChevronUp className="h-4 w-4" /> 升级
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={isClosed || action.isPending}
            onClick={() => action.mutate("resolve")}
          >
            <Check className="h-4 w-4" /> 解决
          </Button>
        </div>
      </div>

      {/* 时间线 */}
      <Card>
        <CardHeader>
          <CardTitle>时间线</CardTitle>
        </CardHeader>
        <CardContent>
          {items.length === 0 ? (
            <EmptyState title="暂无时间线记录" />
          ) : (
            <ol className="relative space-y-4 border-l pl-6">
              {items.map((it) => (
                <li key={it.id} className="relative">
                  {/* 节点圆点 */}
                  <span
                    className={cn(
                      "absolute -left-[1.65rem] top-1 h-2.5 w-2.5 rounded-full ring-4 ring-card",
                      timelineDotColor(it.type),
                    )}
                  />
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm font-medium">
                      {it.content}
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {formatTime(it.timestamp)}
                    </span>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {actorLabel(it.actor)} · 来源 {it.source}
                  </div>
                </li>
              ))}
            </ol>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft className="h-4 w-4" /> 返回列表
    </button>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-4 p-6">
      <Skeleton className="h-4 w-24" />
      <div className="space-y-2">
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
      </div>
      <Skeleton className="h-64 w-full" />
    </div>
  );
}

/** timelineDotColor 时间线节点圆点颜色（按类型）。 */
function timelineDotColor(type: TimelineType): string {
  switch (type) {
    case "ack":
      return "bg-status-acked";
    case "resolved":
      return "bg-status-resolved";
    case "escalated":
      return "bg-status-escalated";
    case "incident_created":
      return "bg-status-triggered";
    default:
      return "bg-muted-foreground";
  }
}

/** actorLabel 时间线 actor 文案。 */
function actorLabel(actor?: { kind?: string; id?: string; name?: string }) {
  if (!actor) return "系统";
  if (actor.name) return actor.name;
  switch (actor.kind) {
    case "user":
      return `用户 ${actor.id ?? "?"}`;
    case "ai":
      return "AI";
    case "integration":
      return "集成";
    default:
      return "系统";
  }
}
