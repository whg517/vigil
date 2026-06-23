/**
 * Postmortems —— 复盘页（能力域 12）。
 * 列表（裸数组）+ 详情（章节 + ActionItems CRUD + 状态流转）+ 从事件起草。
 * 后端：GET /postmortems，GET /postmortems/:id，PATCH /postmortems/:id/transition，
 *       POST /postmortems/:id/action-items，PATCH /action-items/:id，
 *       POST /incidents/:id/postmortem/draft。
 */
import { useState } from "react";
import { ArrowLeft, ClipboardList, Plus, FileText } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAddActionItem,
  useGenerateDraft,
  usePostmortem,
  usePostmortems,
  useTransitionPostmortem,
  useUpdateActionItem,
} from "@/hooks/postmortems";
import { formatTime } from "@/lib/format";
import { extractError } from "@/lib/http";
import type { ActionItem, PostmortemStatus } from "@/lib/types";

const STATUS_LABEL: Record<PostmortemStatus, string> = {
  draft: "草稿",
  in_review: "评审中",
  published: "已发布",
  archived: "已归档",
};
const STATUS_VARIANT: Record<PostmortemStatus, "default" | "secondary" | "outline"> = {
  draft: "secondary",
  in_review: "outline",
  published: "default",
  archived: "outline",
};

export function Postmortems() {
  const { data, isLoading, isError } = usePostmortems();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [drafting, setDrafting] = useState(false);

  if (selected) return <PostmortemDetail id={selected} onBack={() => setSelected(undefined)} />;

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">复盘</h1>
          <p className="text-sm text-muted-foreground">
            闭环学习：从事件起草复盘 → 结构化 → 改进项跟踪 → 知识沉淀。
          </p>
        </div>
        <Button onClick={() => setDrafting(true)}>
          <Plus className="mr-1 h-4 w-4" /> 从事件起草
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
              icon={<ClipboardList className="h-8 w-8" />}
              title="还没有复盘"
              description="事件解决后可起草复盘，让复盘不再是 4 小时苦差。"
            />
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-3">
          {data.map((pm) => (
            <Card
              key={pm.id}
              className="cursor-pointer transition-colors hover:bg-accent/30"
              onClick={() => setSelected(pm.id)}
            >
              <CardContent className="flex items-center justify-between p-4">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium">复盘 #{pm.id}</span>
                    <Badge variant={STATUS_VARIANT[pm.status]}>{STATUS_LABEL[pm.status]}</Badge>
                    <span className="text-xs text-muted-foreground">事件 #{pm.incident?.id}</span>
                  </div>
                </div>
                <span className="text-xs text-muted-foreground">{formatTime(pm.created_at)}</span>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {drafting && (
        <DraftDialog
          onClose={() => setDrafting(false)}
          onCreated={(pmId) => {
            setDrafting(false);
            setSelected(pmId);
          }}
        />
      )}
    </div>
  );
}

/** PostmortemDetail 详情：章节 + 改进项 + 状态流转。 */
function PostmortemDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { data: pm, isLoading, isError } = usePostmortem(id);
  const transition = useTransitionPostmortem(id);
  const addAction = useAddActionItem(id);
  const updateAction = useUpdateActionItem(id);
  const [newItem, setNewItem] = useState("");

  if (isLoading) return <div className="p-6"><Skeleton className="h-40 w-full" /></div>;
  if (isError || !pm) {
    return (
      <div className="p-6">
        <Button variant="ghost" onClick={onBack}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
        <EmptyState title="复盘不存在" />
      </div>
    );
  }

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回列表
        </Button>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">状态</span>
          <Select
            value={pm.status}
            onChange={(e) => transition.mutate(e.target.value as PostmortemStatus)}
            className="w-32"
            disabled={transition.isPending}
          >
            {(Object.keys(STATUS_LABEL) as PostmortemStatus[]).map((s) => (
              <option key={s} value={s}>{STATUS_LABEL[s]}</option>
            ))}
          </Select>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">复盘 #{pm.id}</h1>
        <Badge variant={STATUS_VARIANT[pm.status]}>{STATUS_LABEL[pm.status]}</Badge>
        <span className="text-sm text-muted-foreground">关联事件 #{pm.incident?.id}</span>
      </div>

      {pm.sections && Object.keys(pm.sections).length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FileText className="h-4 w-4" /> 章节
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {Object.entries(pm.sections).map(([k, v]) => (
              <div key={k}>
                <div className="text-xs font-medium text-muted-foreground">{k}</div>
                <p className="mt-1 whitespace-pre-wrap text-sm">{String(v)}</p>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">改进项</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {pm.action_items && pm.action_items.length > 0 ? (
            pm.action_items.map((ai) => (
              <ActionItemRow key={ai.id} ai={ai} onUpdate={(body) => updateAction.mutate({ id: ai.id, body })} />
            ))
          ) : (
            <p className="text-sm text-muted-foreground">暂无改进项。</p>
          )}
          <form
            className="flex gap-2 pt-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (!newItem) return;
              addAction.mutate({ description: newItem }, { onSuccess: () => setNewItem("") });
            }}
          >
            <Input value={newItem} onChange={(e) => setNewItem(e.target.value)} placeholder="添加改进项…" />
            <Button type="submit" size="sm" disabled={addAction.isPending || !newItem}>添加</Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

/** ActionItemRow 单个改进项 + 状态切换。 */
function ActionItemRow({
  ai,
  onUpdate,
}: {
  ai: ActionItem;
  onUpdate: (body: Partial<ActionItem>) => void;
}) {
  const STATUS: ActionItem["status"][] = ["open", "in_progress", "done"];
  return (
    <div className="flex items-center gap-2 rounded-md border p-2">
      <span className="flex-1 text-sm">{ai.description}</span>
      <Select
        value={ai.status}
        onChange={(e) => onUpdate({ status: e.target.value as ActionItem["status"] })}
        className="w-32"
      >
        {STATUS.map((s) => (
          <option key={s} value={s}>{s}</option>
        ))}
      </Select>
      {ai.tracker_url && (
        <a href={ai.tracker_url} target="_blank" rel="noreferrer" className="text-xs text-primary hover:underline">
          工单
        </a>
      )}
    </div>
  );
}

/** DraftDialog 从事件 ID 生成复盘草稿。 */
function DraftDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (pmId: number) => void;
}) {
  const gen = useGenerateDraft();
  const [incidentId, setIncidentId] = useState("");
  const parsedId = Number(incidentId);
  // 合法性：非空、是数字、且为正整数（0 不是有效事件 ID）
  const valid = incidentId !== "" && Number.isInteger(parsedId) && parsedId > 0;
  const errMsg = gen.error ? extractError(gen.error) : null;

  return (
    <Dialog open onClose={onClose} title="从事件起草复盘" description="基于事件时间线 + AI 起草结构化复盘（人校对）。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!valid) return;
          gen.mutate(parsedId, { onSuccess: (pm) => onCreated(pm.id) });
        }}
      >
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">事件 ID</span>
          <Input
            type="number"
            min={1}
            value={incidentId}
            onChange={(e) => setIncidentId(e.target.value)}
            placeholder="例如 1"
            required
            autoFocus
          />
        </label>
        <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
          事件 ID 可在「事件」列表的编号列查看。无 LLM key 时降级为规则草稿（设计基线第 7 条）。
        </p>
        {errMsg && (
          <p className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            {errMsg}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={gen.isPending || !valid}>
            {gen.isPending ? "起草中..." : "起草"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
