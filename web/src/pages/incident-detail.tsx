import { useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Brain, Check, ChevronUp, GitMerge, Hand, RotateCcw, Sparkles, X } from "lucide-react";
import { useAddTimelineNote, useIncident, useIncidentAction, useIncidents, useMergeIncident, useTimeline } from "@/hooks/incidents";
import { toast } from "sonner";
import { useIncidentWS } from "@/hooks/use-incident-ws";
import {
  useDiagnoseIncident,
  useIncidentInsights,
  useResolveInsight,
  useSimilarIncidents,
} from "@/hooks/ai-diagnose";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { SeverityBadge, StatusBadge } from "@/lib/badges";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  AIInsight,
  AIInsightStatus,
  DiagnoseResult,
  TimelineType,
} from "@/lib/types";

/**
 * IncidentDetail —— 事件详情页。
 * 头部信息 + 操作（确认/解决/升级）+ 时间线。
 * 对应后端 GET /incidents/:id、GET /incidents/:id/timeline、POST .../ack|resolve|escalate。
 */
export function IncidentDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
	const incId = Number(id);

	// WebSocket 实时同步（能力域 8）：incident 变更自动刷新，无需手动刷新页面
	useIncidentWS(incId);

	const { data: inc, isLoading } = useIncident(incId);
  const { data: tl } = useTimeline(incId);
  const action = useIncidentAction(incId);
  const [merging, setMerging] = useState(false);
  const addNote = useAddTimelineNote(incId);
  const [note, setNote] = useState("");

  // 提交人工备注（联系了谁、试了什么）——处置现场的记录必须能沉淀到时间线
  const submitNote = () => {
    const content = note.trim();
    if (!content) return;
    addNote.mutate(content, {
      onSuccess: () => {
        setNote("");
        toast.success(t("incidentDetail.noteAdded"));
      },
    });
  };

  if (isLoading) return <DetailSkeleton />;
  if (!inc) {
    return (
      <div className="p-6">
        <BackButton onClick={() => navigate("/incidents")} />
        <EmptyState
          title={t("incidentDetail.notFoundTitle")}
          description={t("incidentDetail.notFoundDesc", { id })}
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
            <span>{t("incidentDetail.priority", { value: inc.priority.toUpperCase() })}</span>
            <span>{t("incidentDetail.currentLevel", { level: inc.current_level })}</span>
            <span>{t("incidentDetail.escalatedCount", { count: inc.escalated_count })}</span>
            <span>{t("incidentDetail.createdAt", { time: formatTime(inc.created_at) })}</span>
            {inc.resolved_at && (
              <span>{t("incidentDetail.resolvedAt", { time: formatTime(inc.resolved_at) })}</span>
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
              <RotateCcw className="h-4 w-4" /> {t("incidentDetail.actionReopen")}
            </Button>
          ) : (
            <>
              <Button
                variant="default"
                size="sm"
                disabled={isAcked || action.isPending}
                onClick={() => action.mutate("ack")}
              >
                <Hand className="h-4 w-4" /> {t("incidentDetail.actionAck")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={action.isPending}
                onClick={() => action.mutate("escalate")}
              >
                <ChevronUp className="h-4 w-4" /> {t("incidentDetail.actionEscalate")}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={action.isPending}
                onClick={() => action.mutate("resolve")}
              >
                <Check className="h-4 w-4" /> {t("incidentDetail.actionResolve")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={action.isPending}
                onClick={() => setMerging(true)}
              >
                <GitMerge className="h-4 w-4" /> {t("incidentDetail.actionMerge")}
              </Button>
            </>
          )}
        </div>
      </div>

      {/* 合并对话框（能力域 3 去重合并，不可逆）：选源单并入本单 */}
      {merging && (
        <MergeDialog
          incidentId={incId}
          targetNumber={inc.number}
          onClose={() => setMerging(false)}
        />
      )}

      {/* AI 诊断（能力域 11）：根因诊断 + human-in-the-loop 确认 + 相似事件 */}
      <AIDiagnoseCard incidentId={incId} />

      {/* 时间线 */}
      <Card>
        <CardHeader>
          <CardTitle>{t("incidentDetail.timelineTitle")}</CardTitle>
        </CardHeader>
        <CardContent>
          {/* 人工备注输入：POST /incidents/:id/timeline（note_added，actor 服务端回填） */}
          <div className="mb-4 flex gap-2">
            <Input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") submitNote();
              }}
              placeholder={t("incidentDetail.notePlaceholder")}
              disabled={addNote.isPending}
            />
            <Button
              variant="outline"
              onClick={submitNote}
              disabled={addNote.isPending || !note.trim()}
            >
              {t("incidentDetail.noteSubmit")}
            </Button>
          </div>
          {items.length === 0 ? (
            <EmptyState title={t("incidentDetail.timelineEmpty")} />
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
                    {t("incidentDetail.timelineActorSource", {
                      actor: actorLabel(t, it.actor),
                      source: it.source,
                    })}
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
  const { t } = useTranslation();
  return (
    <button
      onClick={onClick}
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft className="h-4 w-4" /> {t("incidentDetail.backToList")}
    </button>
  );
}

/**
 * MergeDialog 合并对话框（能力域 3 去重合并，不可逆）。
 * 勾选一个或多个源事件并入本单：源单被关闭、events/responders 转移到本单。
 * 候选列表排除本单自身与已关闭/已解决单（这些不适合作为待合并源）。
 */
function MergeDialog({
  incidentId,
  targetNumber,
  onClose,
}: {
  incidentId: number;
  targetNumber: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const merge = useMergeIncident(incidentId);
  // 拉取活跃事件作为候选（limit 从大取，前端再过滤本单/已关闭单）。
  const { data, isLoading } = useIncidents({ limit: 100 });
  const [selected, setSelected] = useState<number[]>([]);

  const candidates = (data?.items ?? []).filter(
    (i) => i.id !== incidentId && i.status !== "resolved" && i.status !== "closed",
  );

  const toggle = (id: number) =>
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  const onConfirm = () => {
    if (selected.length === 0) return;
    merge.mutate(selected, { onSuccess: onClose });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("incidentDetail.mergeTitle", { number: targetNumber })}
      description={t("incidentDetail.mergeDesc")}
    >
      <div className="space-y-3">
        <div className="rounded-md bg-destructive/10 p-2 text-xs text-destructive">
          {t("incidentDetail.mergeWarning")}
        </div>
        {isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : candidates.length === 0 ? (
          <EmptyState
            title={t("incidentDetail.mergeEmptyTitle")}
            description={t("incidentDetail.mergeEmptyDesc")}
          />
        ) : (
          <div className="max-h-72 space-y-1 overflow-auto pr-1">
            {candidates.map((c) => (
              <label
                key={c.id}
                className="flex cursor-pointer items-center gap-2 rounded-md border p-2 text-sm hover:bg-muted/40"
              >
                <input
                  type="checkbox"
                  checked={selected.includes(c.id)}
                  onChange={() => toggle(c.id)}
                  className="h-4 w-4"
                />
                <span className="font-mono text-xs text-muted-foreground">{c.number}</span>
                <SeverityBadge severity={c.severity} />
                <span className="flex-1 truncate">{c.title}</span>
                <StatusBadge status={c.status} />
              </label>
            ))}
          </div>
        )}
        <div className="flex items-center justify-between pt-1">
          <span className="text-xs text-muted-foreground">
            {t("incidentDetail.mergeSelectedCount", { count: selected.length })}
          </span>
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={merge.isPending || selected.length === 0}
              onClick={onConfirm}
            >
              {merge.isPending
                ? t("incidentDetail.merging")
                : t("incidentDetail.mergeConfirm", { count: selected.length })}
            </Button>
          </div>
        </div>
      </div>
    </Dialog>
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
function actorLabel(
  t: (key: string, opts?: Record<string, unknown>) => string,
  actor?: { kind?: string; id?: string; name?: string },
) {
  if (!actor) return t("incidentDetail.actorSystem");
  if (actor.name) return actor.name;
  switch (actor.kind) {
    case "user":
      return t("incidentDetail.actorUser", { id: actor.id ?? "?" });
    case "ai":
      return "AI";
    case "integration":
      return t("incidentDetail.actorIntegration");
    default:
      return t("incidentDetail.actorSystem");
  }
}

/**
 * AIDiagnoseCard AI 诊断卡片（能力域 11）。
 * 触发 LLM 根因诊断 → 展示根因/置信度/evidence → 人确认或拒绝（human-in-the-loop）。
 * 未启用 LLM 时后端返回 {status:"disabled"}，显示降级提示。
 */
function AIDiagnoseCard({ incidentId }: { incidentId: number }) {
  const { t } = useTranslation();
  const diagnose = useDiagnoseIncident(incidentId);
  const resolve = useResolveInsight(incidentId);
  const insights = useIncidentInsights(incidentId);
  const [result, setResult] = useState<DiagnoseResult | null>(null);
  const [disabledMsg, setDisabledMsg] = useState<string | null>(null);
  const [showSimilar, setShowSimilar] = useState(false);
  const similar = useSimilarIncidents(incidentId, showSimilar);

  const onDiagnose = () => {
    diagnose.mutate(incidentId, {
      onSuccess: (data) => {
        if ("status" in data && data.status === "disabled") {
          setDisabledMsg(data.message || t("incidentDetail.aiDisabledFallback"));
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

  const historyItems = insights.data ?? [];

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <Brain className="h-4 w-4" /> {t("incidentDetail.aiTitle")}
        </CardTitle>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={similar.isFetching}
            onClick={() => setShowSimilar((v) => !v)}
          >
            <Sparkles className="mr-1 h-3.5 w-3.5" />
            {showSimilar
              ? t("incidentDetail.hideSimilar")
              : t("incidentDetail.showSimilar")}
          </Button>
          <Button size="sm" onClick={onDiagnose} disabled={diagnose.isPending}>
            {diagnose.isPending
              ? t("incidentDetail.diagnosing")
              : t("incidentDetail.diagnose")}
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
                {t("incidentDetail.confidence", {
                  value: Math.round(result.confidence * 100),
                })}
              </Badge>
              <div className="flex-1">
                <div className="text-xs font-medium text-muted-foreground">
                  {t("incidentDetail.rootCause")}
                </div>
                <p className="text-sm">{result.root_cause}</p>
              </div>
            </div>

            {result.evidence && result.evidence.length > 0 && (
              <details className="rounded-md border p-2">
                <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                  {t("incidentDetail.evidence", { count: result.evidence.length })}
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
              <span className="text-xs text-muted-foreground">{t("incidentDetail.helpfulPrompt")}</span>
              <Button
                size="sm"
                variant="outline"
                disabled={resolve.isPending}
                onClick={() => onResolve(true)}
              >
                <Check className="mr-1 h-3.5 w-3.5" /> {t("incidentDetail.accept")}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={resolve.isPending}
                onClick={() => onResolve(false)}
              >
                <X className="mr-1 h-3.5 w-3.5" /> {t("incidentDetail.reject")}
              </Button>
            </div>
          </div>
        )}

        {/* 历史 AI 洞察（T3.1 可读持久化）：诊断产出落库后持久呈现，状态随 accept/reject 变更持久。 */}
        {historyItems.length > 0 && (
          <div className="space-y-2 border-t pt-3">
            <div className="text-xs font-medium text-muted-foreground">
              {t("incidentDetail.historyInsights", { count: historyItems.length })}
            </div>
            <ul className="space-y-2">
              {historyItems.map((ins) => (
                <InsightHistoryItem
                  key={ins.id}
                  insight={ins}
                  onResolve={(accepted) =>
                    resolve.mutate({ insightId: ins.id, accepted })
                  }
                  resolving={resolve.isPending}
                />
              ))}
            </ul>
          </div>
        )}

        {/* 相似历史事件 */}
        {showSimilar && (
          <div className="space-y-2 border-t pt-3">
            <div className="text-xs font-medium text-muted-foreground">{t("incidentDetail.similarTitle")}</div>
            {similar.isLoading ? (
              <Skeleton className="h-12 w-full" />
            ) : !similar.data || similar.data.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("incidentDetail.similarEmpty")}</p>
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

/** insightStatusMeta AI 洞察状态的文案与 Badge variant（human-in-the-loop 生命周期）。 */
function insightStatusMeta(
  t: (key: string, opts?: Record<string, unknown>) => string,
  status: AIInsightStatus,
): {
  label: string;
  variant: "default" | "warning" | "outline" | "destructive";
} {
  switch (status) {
    case "applied":
      return { label: t("incidentDetail.insightStatus.applied"), variant: "default" };
    case "accepted":
      return { label: t("incidentDetail.insightStatus.accepted"), variant: "default" };
    case "rejected":
      return { label: t("incidentDetail.insightStatus.rejected"), variant: "destructive" };
    default:
      return { label: t("incidentDetail.insightStatus.suggested"), variant: "warning" };
  }
}

/** insightTypeLabel AI 洞察类型中文文案。 */
function insightTypeLabel(
  t: (key: string, opts?: Record<string, unknown>) => string,
  type: string,
): string {
  switch (type) {
    case "root_cause_hint":
      return t("incidentDetail.insightType.rootCauseHint");
    case "severity_adjustment":
      return t("incidentDetail.insightType.severityAdjustment");
    case "dedup_suggestion":
      return t("incidentDetail.insightType.dedupSuggestion");
    case "similar_incident":
      return t("incidentDetail.insightType.similarIncident");
    case "draft_summary":
      return t("incidentDetail.insightType.draftSummary");
    case "postmortem_draft":
      return t("incidentDetail.insightType.postmortemDraft");
    default:
      return type;
  }
}

/** insightSummary 从 content 提取一句可读摘要（root_cause / target_severity / 兜底 JSON）。 */
function insightSummary(
  t: (key: string, opts?: Record<string, unknown>) => string,
  ins: AIInsight,
): string {
  const content = ins.content ?? {};
  if (typeof content.root_cause === "string") return content.root_cause;
  if (typeof content.target_severity === "string")
    return t("incidentDetail.severitySuggestion", { severity: content.target_severity });
  const keys = Object.keys(content);
  return keys.length > 0 ? JSON.stringify(content) : t("incidentDetail.noContent");
}

/**
 * InsightHistoryItem 单条历史 AI 洞察（T3.1）。
 * 展示类型/置信度/状态/内容；仍处于 suggested 的可就地 accept/reject（状态持久化）。
 */
function InsightHistoryItem({
  insight,
  onResolve,
  resolving,
}: {
  insight: AIInsight;
  onResolve: (accepted: boolean) => void;
  resolving: boolean;
}) {
  const { t } = useTranslation();
  const meta = insightStatusMeta(t, insight.status);
  const pending = insight.status === "suggested";
  return (
    <li className="rounded-md border p-2 text-sm">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Badge variant="outline">{insightTypeLabel(t, insight.type)}</Badge>
          <span className="text-xs text-muted-foreground">
            {t("incidentDetail.confidence", {
              value: Math.round((insight.confidence ?? 0) * 100),
            })}
          </span>
        </div>
        <Badge variant={meta.variant}>{meta.label}</Badge>
      </div>
      <p className="mt-1 text-sm">{insightSummary(t, insight)}</p>
      <div className="mt-1 flex items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          {formatTime(insight.created_at)}
        </span>
        {pending && (
          <div className="flex items-center gap-1">
            <Button
              size="sm"
              variant="outline"
              disabled={resolving}
              onClick={() => onResolve(true)}
            >
              <Check className="mr-1 h-3.5 w-3.5" /> {t("incidentDetail.accept")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={resolving}
              onClick={() => onResolve(false)}
            >
              <X className="mr-1 h-3.5 w-3.5" /> {t("incidentDetail.reject")}
            </Button>
          </div>
        )}
      </div>
    </li>
  );
}
