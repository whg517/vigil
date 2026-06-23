import { useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { ArrowLeft, Brain, Check, ChevronUp, Hand, RotateCcw, Sparkles, X } from "lucide-react";
import { useIncident, useIncidentAction, useTimeline } from "@/hooks/incidents";
import { useIncidentWS } from "@/hooks/use-incident-ws";
import {
  useDiagnoseIncident,
  useResolveInsight,
  useSimilarIncidents,
} from "@/hooks/ai-diagnose";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, StatusBadge } from "@/lib/badges";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { DiagnoseResult, TimelineType } from "@/lib/types";

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
          {isClosed ? (
            // 已解决/已关闭：显示重新打开（对称于 resolve）
            <Button
              variant="default"
              size="sm"
              disabled={action.isPending}
              onClick={() => action.mutate("reopen")}
            >
              <RotateCcw className="h-4 w-4" /> 重新打开
            </Button>
          ) : (
            <>
              <Button
                variant="default"
                size="sm"
                disabled={isAcked || action.isPending}
                onClick={() => action.mutate("ack")}
              >
                <Hand className="h-4 w-4" /> 确认
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={action.isPending}
                onClick={() => action.mutate("escalate")}
              >
                <ChevronUp className="h-4 w-4" /> 升级
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={action.isPending}
                onClick={() => action.mutate("resolve")}
              >
                <Check className="h-4 w-4" /> 解决
              </Button>
            </>
          )}
        </div>
      </div>

      {/* AI 诊断（能力域 11）：根因诊断 + human-in-the-loop 确认 + 相似事件 */}
      <AIDiagnoseCard incidentId={incId} />

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

/**
 * AIDiagnoseCard AI 诊断卡片（能力域 11）。
 * 触发 LLM 根因诊断 → 展示根因/置信度/evidence → 人确认或拒绝（human-in-the-loop）。
 * 未启用 LLM 时后端返回 {status:"disabled"}，显示降级提示。
 */
function AIDiagnoseCard({ incidentId }: { incidentId: number }) {
  const diagnose = useDiagnoseIncident();
  const resolve = useResolveInsight(incidentId);
  const [result, setResult] = useState<DiagnoseResult | null>(null);
  const [disabledMsg, setDisabledMsg] = useState<string | null>(null);
  const [showSimilar, setShowSimilar] = useState(false);
  const similar = useSimilarIncidents(incidentId, showSimilar);

  const onDiagnose = () => {
    diagnose.mutate(incidentId, {
      onSuccess: (data) => {
        if ("status" in data && data.status === "disabled") {
          setDisabledMsg(data.message || "AI 诊断未启用（无 LLM）");
          setResult(null);
        } else {
          setResult(data as DiagnoseResult);
          setDisabledMsg(null);
        }
      },
    });
  };

  const onResolve = (accepted: boolean) => {
    if (!result?.insight_id) return;
    resolve.mutate(
      { insightId: result.insight_id, accepted },
      { onSuccess: () => setResult(null) },
    );
  };

  const confidenceVariant = (c: number) => {
    if (c >= 0.8) return "default" as const;
    if (c >= 0.5) return "warning" as const;
    return "outline" as const;
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <Brain className="h-4 w-4" /> AI 诊断
        </CardTitle>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={similar.isFetching}
            onClick={() => setShowSimilar((v) => !v)}
          >
            <Sparkles className="mr-1 h-3.5 w-3.5" />
            {showSimilar ? "隐藏相似事件" : "相似事件"}
          </Button>
          <Button size="sm" onClick={onDiagnose} disabled={diagnose.isPending}>
            {diagnose.isPending ? "诊断中…" : "诊断"}
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {/* 降级提示 */}
        {disabledMsg && (
          <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">{disabledMsg}</p>
        )}

        {/* 诊断结果 */}
        {result && (
          <div className="space-y-3">
            <div className="flex items-start gap-2">
              <Badge variant={confidenceVariant(result.confidence)}>
                置信度 {Math.round(result.confidence * 100)}%
              </Badge>
              <div className="flex-1">
                <div className="text-xs font-medium text-muted-foreground">根因线索</div>
                <p className="text-sm">{result.root_cause}</p>
              </div>
            </div>

            {result.evidence && result.evidence.length > 0 && (
              <details className="rounded-md border p-2">
                <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                  依据（{result.evidence.length} 条）
                </summary>
                <ul className="mt-2 space-y-1">
                  {result.evidence.map((ev, i) => (
                    <li key={i} className="text-xs text-muted-foreground">
                      {Object.entries(ev).map(([k, v]) => `${k}: ${String(v)}`).join(" · ")}
                    </li>
                  ))}
                </ul>
              </details>
            )}

            {/* human-in-the-loop：人确认/拒绝 */}
            <div className="flex items-center gap-2 border-t pt-2">
              <span className="text-xs text-muted-foreground">这条诊断对你有帮助吗？</span>
              <Button
                size="sm"
                variant="outline"
                disabled={resolve.isPending}
                onClick={() => onResolve(true)}
              >
                <Check className="mr-1 h-3.5 w-3.5" /> 采纳
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={resolve.isPending}
                onClick={() => onResolve(false)}
              >
                <X className="mr-1 h-3.5 w-3.5" /> 拒绝
              </Button>
            </div>
          </div>
        )}

        {/* 相似历史事件 */}
        {showSimilar && (
          <div className="space-y-2 border-t pt-3">
            <div className="text-xs font-medium text-muted-foreground">相似历史事件</div>
            {similar.isLoading ? (
              <Skeleton className="h-12 w-full" />
            ) : !similar.data || similar.data.length === 0 ? (
              <p className="text-xs text-muted-foreground">未找到相似事件。</p>
            ) : (
              <ul className="space-y-1">
                {similar.data.map((s) => (
                  <li
                    key={s.id}
                    className="flex items-center justify-between rounded-md border p-2 text-sm"
                  >
                    <div className="flex items-center gap-2">
                      <SeverityBadge severity={s.severity} />
                      <span className="truncate">{s.title}</span>
                    </div>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {s.status} · {formatTime(s.created_at)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
