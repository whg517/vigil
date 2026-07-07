/**
 * Runbooks —— 处置手册页（能力域 9）。
 * 列表 + 详情（content_markdown 用 react-markdown 渲染）+ 创建 + execute（human-in-the-loop）。
 * 后端：GET/POST /runbooks，GET/DELETE /runbooks/:id，POST /runbooks/:id/execute。
 */
import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import ReactMarkdown from "react-markdown";
import {
  ArrowLeft,
  BookOpen,
  CheckCircle2,
  MinusCircle,
  Pencil,
  Play,
  Plus,
  ShieldAlert,
  Trash2,
  XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import {
  useCreateRunbook,
  useDeleteRunbook,
  useExecuteRunbook,
  useRunbook,
  useRunbooks,
  useUpdateRunbook,
} from "@/hooks/runbooks";
import { formatTime } from "@/lib/format";
import type { Runbook, RunbookExecuteResult, RunbookStepResult } from "@/lib/types";

export function Runbooks() {
  const { t } = useTranslation();
  const { data, isLoading, isError } = useRunbooks();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [creating, setCreating] = useState(false);

  // key={selected}：切换 Runbook 时强制重挂载，避免上一个 Runbook 的执行结果（exec.data）串到另一个。
  if (selected)
    return <RunbookDetail key={selected} id={selected} onBack={() => setSelected(undefined)} />;

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("runbooks.title")}</h1>
          <p className="text-sm text-muted-foreground">
            {t("runbooks.subtitle")}
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("runbooks.create")}
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : isError ? null : !data || data.length === 0 ? (
        <Card>
          <CardContent className="p-6">
            <EmptyState
              icon={<BookOpen className="h-8 w-8" />}
              title={t("runbooks.emptyTitle")}
              description={t("runbooks.emptyDescription")}
            />
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-3">
          {data.map((r) => (
            <Card
              key={r.id}
              className="cursor-pointer transition-colors hover:bg-accent/30"
              onClick={() => setSelected(r.id)}
            >
              <CardContent className="flex items-center justify-between p-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{r.name}</span>
                    <Badge variant="outline" className="text-xs">
                      {r.type === "executable" ? t("runbooks.typeExecutable") : t("runbooks.typeDocument")}
                    </Badge>
                  </div>
                  {r.content_markdown && (
                    <p className="mt-1 truncate text-xs text-muted-foreground">
                      {r.content_markdown.slice(0, 80)}
                    </p>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground">{formatTime(r.created_at)}</span>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {creating && <RunbookFormDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** RunbookDetail 详情：markdown 渲染 + 执行（human-in-the-loop）+ 编辑 + 删除。 */
function RunbookDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { t } = useTranslation();
  const { data: rb, isLoading, isError } = useRunbook(id);
  const del = useDeleteRunbook();
  const exec = useExecuteRunbook();
  const [executing, setExecuting] = useState(false);
  const [editing, setEditing] = useState(false);
  const [incidentId, setIncidentId] = useState("");
  // 写操作审批：默认不批准（human-in-the-loop）。不勾选 = 干跑（写步骤被跳过），
  // 勾选 = 明确批准执行写操作。绝不恒发 approved:true（安全红线，见 C.5.1）。
  const [approveWrites, setApproveWrites] = useState(false);

  const closeExecuting = () => {
    setExecuting(false);
    setApproveWrites(false); // 复位：每次执行都需重新做审批决策
  };

  if (isLoading) return <div className="p-6"><Skeleton className="h-40 w-full" /></div>;
  if (isError || !rb) {
    return (
      <div className="p-6">
        <Button variant="ghost" onClick={onBack}><ArrowLeft className="mr-1 h-4 w-4" />{t("runbooks.back")}</Button>
        <EmptyState title={t("runbooks.notFound")} />
      </div>
    );
  }

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="mr-1 h-4 w-4" />{t("runbooks.backToList")}
        </Button>
        <div className="flex gap-2">
          <Button onClick={() => setExecuting(true)}>
            <Play className="mr-1 h-4 w-4" /> {t("runbooks.execute")}
          </Button>
          <Button variant="outline" onClick={() => setEditing(true)}>
            <Pencil className="mr-1 h-4 w-4" /> {t("common.edit")}
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              del.mutate(id, { onSuccess: onBack });
            }}
            disabled={del.isPending}
          >
            <Trash2 className="mr-1 h-4 w-4" /> {t("common.delete")}
          </Button>
        </div>
      </div>

      <div>
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{rb.name}</h1>
          <Badge variant="outline">{rb.type === "executable" ? t("runbooks.typeExecutable") : t("runbooks.typeDocument")}</Badge>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("runbooks.steps")}</CardTitle>
        </CardHeader>
        <CardContent>
          {rb.content_markdown ? (
            <div className="prose prose-sm max-w-none text-sm">
              <ReactMarkdown>{rb.content_markdown}</ReactMarkdown>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">{t("runbooks.noContent")}</p>
          )}
        </CardContent>
      </Card>

      {exec.data && <ExecutionResult result={exec.data} />}

      {executing && (
        <Dialog open onClose={closeExecuting} title={t("runbooks.executeDialogTitle")} description={t("runbooks.executeDialogDescription")}>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              exec.mutate(
                { id, incidentId: Number(incidentId), approved: approveWrites },
                { onSuccess: closeExecuting },
              );
            }}
          >
            <label className="block space-y-1">
              <span className="text-xs font-medium text-muted-foreground">{t("runbooks.incidentId")}</span>
              <Input
                type="number"
                value={incidentId}
                onChange={(e) => setIncidentId(e.target.value)}
                required
                placeholder="42"
              />
            </label>
            <label className="flex items-start gap-2 rounded-md border border-amber-300/60 bg-amber-50 p-2 text-xs dark:border-amber-500/30 dark:bg-amber-950/30">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0 accent-amber-600"
                checked={approveWrites}
                onChange={(e) => setApproveWrites(e.target.checked)}
              />
              <span className="text-amber-900 dark:text-amber-200">
                <Trans i18nKey="runbooks.approveWritesLabel">
                  <span className="font-medium">我确认执行写操作</span>
                  （回滚/扩容等）。不勾选则仅干跑：诊断类（readonly）步骤照常执行，写操作步骤将被跳过。
                </Trans>
              </span>
            </label>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={closeExecuting}>{t("common.cancel")}</Button>
              <Button type="submit" disabled={exec.isPending || !incidentId}>
                {approveWrites ? t("runbooks.confirmExecuteWrites") : t("runbooks.executeDryRun")}
              </Button>
            </div>
          </form>
        </Dialog>
      )}

      {editing && (
        <RunbookFormDialog runbook={rb} onClose={() => setEditing(false)} />
      )}
    </div>
  );
}

/**
 * ExecutionResult 执行结果面板：逐步渲染每步成败/输出/耗时，并高亮
 * “写步骤未获批准被阻断（pending_approval）”与“中止（aborted）”，
 * 呼应写审批闸门修复——让审批/阻断结果在 UI 可见（audit B20 / user-journeys C.5.2）。
 */
function ExecutionResult({ result }: { result: RunbookExecuteResult }) {
  const { t } = useTranslation();
  const steps = result.steps ?? [];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          {t("runbooks.executionResult")}
          {result.aborted ? (
            <Badge variant="destructive" className="text-xs">{t("runbooks.aborted")}</Badge>
          ) : result.pending_approval ? (
            <Badge variant="outline" className="border-amber-400 text-xs text-amber-700 dark:text-amber-300">
              {t("runbooks.partialPendingApproval")}
            </Badge>
          ) : (
            <Badge variant="outline" className="text-xs">{t("runbooks.completed")}</Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {result.aborted && result.reason && (
          <p className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            {t("runbooks.abortReason", { reason: result.reason })}
          </p>
        )}
        {result.pending_approval && (
          <p className="flex items-start gap-2 rounded-md border border-amber-300/60 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-950/30 dark:text-amber-200">
            <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
            {t("runbooks.pendingApprovalHint")}
          </p>
        )}
        {steps.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("runbooks.noExecutableSteps")}</p>
        ) : (
          <ol className="space-y-2">
            {steps.map((s, i) => (
              <StepResultRow key={s.step_id || i} step={s} index={i} />
            ))}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}

/** StepResultRow 单步结果行：状态图标 + 名称/动作 + 输出/错误 + 耗时。 */
function StepResultRow({ step, index }: { step: RunbookStepResult; index: number }) {
  const { t } = useTranslation();
  // 三态：跳过（写步骤被阻断，未获批）> 失败 > 成功。
  const skipped = step.skipped;
  const failed = !skipped && !step.success;
  // 成功/跳过只有一条信息用 detail 渲染；失败则单独渲染 error + output（见下）。
  const detail = failed ? step.error : step.output;
  return (
    <li className="rounded-md border p-2">
      <div className="flex items-center gap-2">
        {skipped ? (
          <MinusCircle className="h-4 w-4 shrink-0 text-amber-600" />
        ) : failed ? (
          <XCircle className="h-4 w-4 shrink-0 text-destructive" />
        ) : (
          <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-600" />
        )}
        <span className="font-medium">
          {index + 1}. {step.name || step.step_id || t("runbooks.unnamedStep")}
        </span>
        {step.action && (
          <Badge variant="outline" className="text-[10px]">{step.action}</Badge>
        )}
        {skipped && (
          <Badge variant="outline" className="border-amber-400 text-[10px] text-amber-700 dark:text-amber-300">
            {t("runbooks.writeStepPending")}
          </Badge>
        )}
        <span className="ml-auto text-[10px] text-muted-foreground">{formatDuration(step.duration)}</span>
      </div>
      {failed ? (
        // 失败时同时展示 error（一句话原因）与 output（执行器返回的结构化诊断，
        // 如 HTTP status_code/body）；只显 error 会丢掉状态码/响应体等定位信息（强化 FIX-E）。
        <>
          {step.error && (
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded bg-destructive/10 p-2 text-[11px] text-destructive">
              {step.error}
            </pre>
          )}
          {step.output && (
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded bg-muted/50 p-2 text-[11px] text-muted-foreground">
              {step.output}
            </pre>
          )}
        </>
      ) : (
        detail && (
          <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded bg-muted/50 p-2 text-[11px] text-muted-foreground">
            {detail}
          </pre>
        )
      )}
    </li>
  );
}

/** formatDuration 把 Go time.Duration（纳秒）格式化为可读耗时；0/缺省不显示。 */
function formatDuration(ns?: number): string {
  if (!ns || ns <= 0) return "";
  const ms = ns / 1e6;
  if (ms < 1000) return `${ms.toFixed(ms < 10 ? 1 : 0)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

/**
 * RunbookFormDialog 创建/编辑复用：传 runbook 则编辑。
 * 可编辑 name / type / content_markdown（后端 PATCH 支持这些字段）。
 */
function RunbookFormDialog({ runbook, onClose }: { runbook?: Runbook; onClose: () => void }) {
  const { t } = useTranslation();
  const isEdit = !!runbook;
  const create = useCreateRunbook();
  const update = useUpdateRunbook();
  const [name, setName] = useState(runbook?.name ?? "");
  const [type, setType] = useState<Runbook["type"]>(runbook?.type ?? "document");
  const [content, setContent] = useState(runbook?.content_markdown ?? "");

  const pending = create.isPending || update.isPending;

  return (
    <Dialog
      open
      onClose={onClose}
      title={isEdit ? t("runbooks.editTitle", { name: runbook?.name }) : t("runbooks.create")}
      description={t("runbooks.formDescription")}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (isEdit && runbook) {
            update.mutate({ id: runbook.id, body: { name, type, content_markdown: content } }, { onSuccess: onClose });
          } else {
            create.mutate({ name, type, content_markdown: content }, { onSuccess: onClose });
          }
        }}
      >
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">{t("runbooks.name")}</span>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </label>
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">{t("runbooks.type")}</span>
          <Select value={type} onChange={(e) => setType(e.target.value as Runbook["type"])}>
            <option value="document">{t("runbooks.typeDocumentOption")}</option>
            <option value="executable">{t("runbooks.typeExecutable")}</option>
          </Select>
        </label>
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">{t("runbooks.contentMarkdown")}</span>
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            rows={8}
            placeholder={t("runbooks.contentPlaceholder")}
          />
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={pending || !name}>
            {pending ? t("common.submitting") : isEdit ? t("common.save") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
