/**
 * Postmortems —— 复盘页（能力域 12）。
 * 列表（裸数组）+ 详情（章节 + ActionItems CRUD + 状态流转）+ 从事件起草。
 * 后端：GET /postmortems，GET /postmortems/:id，PATCH /postmortems/:id/transition，
 *       POST /postmortems/:id/action-items，PATCH /action-items/:id，
 *       POST /incidents/:id/postmortem/draft。
 */
import { useState } from "react";
import { ArrowLeft, ClipboardList, Plus, FileText, Trash2, Sparkles, Pencil } from "lucide-react";
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
  useAddActionItem,
  useDeleteActionItem,
  useDeletePostmortem,
  useEditSections,
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

/** PostmortemDetail 详情：章节 + 改进项 + 状态流转 + 删除。 */
function PostmortemDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { data: pm, isLoading, isError } = usePostmortem(id);
  const transition = useTransitionPostmortem(id);
  const addAction = useAddActionItem(id);
  const updateAction = useUpdateActionItem(id);
  const deleteAction = useDeleteActionItem(id);
  const del = useDeletePostmortem();
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
          <Button
            variant="outline"
            size="sm"
            disabled={del.isPending}
            onClick={() => {
              if (window.confirm(`确定删除复盘 #${pm.id}？其改进项将一并删除，且不可恢复。`)) {
                del.mutate(pm.id, { onSuccess: onBack });
              }
            }}
          >
            <Trash2 className="mr-1 h-4 w-4" /> 删除复盘
          </Button>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">复盘 #{pm.id}</h1>
        <Badge variant={STATUS_VARIANT[pm.status]}>{STATUS_LABEL[pm.status]}</Badge>
        <span className="text-sm text-muted-foreground">关联事件 #{pm.incident?.id}</span>
      </div>

      <SectionsCard id={pm.id} status={pm.status} sections={pm.sections} />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">改进项</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {pm.action_items && pm.action_items.length > 0 ? (
            pm.action_items.map((ai) => (
              <ActionItemRow
                key={ai.id}
                ai={ai}
                onUpdate={(body) => updateAction.mutate({ id: ai.id, body })}
                onDelete={() => deleteAction.mutate(ai.id)}
                deleting={deleteAction.isPending}
              />
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

// AI_SECTIONS_KEY sections JSON 内保留键：记录仍标记「AI 草拟」的段落名（与后端 engine.go aiSectionsKey 对齐）。
const AI_SECTIONS_KEY = "_ai_sections";
// 段名中文标签（覆盖模板段；未列出的段名原样展示）。
const SECTION_LABEL: Record<string, string> = {
  summary: "摘要",
  impact: "影响",
  timeline: "时间线",
  root_cause: "根因",
  contributing_factors: "促成因素",
  what_went_well: "做得好的",
  what_went_wrong: "做得差的",
  action_items: "改进项",
};
// 只读段：由系统/关联数据自动生成，不走逐段编辑（时间线是事实依据，改进项有独立 CRUD）。
const READONLY_SECTIONS = new Set(["timeline", "action_items"]);

/** SectionsCard 复盘章节卡片：逐段渲染 + 编辑（部分更新，T4.2）。 */
function SectionsCard({
  id,
  status,
  sections,
}: {
  id: number;
  status: PostmortemStatus;
  sections?: Record<string, unknown>;
}) {
  const edit = useEditSections(id);
  if (!sections || Object.keys(sections).length === 0) return null;

  // 定稿锁定：published/archived 章节只读（与后端 ErrPostmortemLocked 门禁一致）。
  const locked = status === "published" || status === "archived";
  const aiSet = new Set(
    Array.isArray(sections[AI_SECTIONS_KEY]) ? (sections[AI_SECTIONS_KEY] as string[]) : [],
  );
  // 过滤保留键（下划线前缀元数据）后逐段渲染。
  const entries = Object.entries(sections).filter(([k]) => !k.startsWith("_"));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <FileText className="h-4 w-4" /> 章节
          {locked && <Badge variant="outline">已定稿·只读</Badge>}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {entries.map(([k, v]) => (
          <SectionRow
            key={k}
            name={k}
            value={v}
            isAI={aiSet.has(k)}
            editable={!locked && !READONLY_SECTIONS.has(k)}
            onSave={(val) => edit.mutate({ [k]: val })}
            saving={edit.isPending}
          />
        ))}
      </CardContent>
    </Card>
  );
}

/**
 * SectionRow 单段：展示 + 就地编辑（[编辑]→[保存]/[取消]，即逐段 accept/edit）。
 * 字符串段用 textarea；字符串数组段（what_went_well/wrong）以换行分隔编辑，保存时回填数组；
 * 其它复杂结构（timeline/action_items）只读 JSON 展示。
 */
function SectionRow({
  name,
  value,
  isAI,
  editable,
  onSave,
  saving,
}: {
  name: string;
  value: unknown;
  isAI: boolean;
  editable: boolean;
  onSave: (val: unknown) => void;
  saving: boolean;
}) {
  const isStringArray = Array.isArray(value) && value.every((x) => typeof x === "string");
  const asText =
    typeof value === "string"
      ? value
      : isStringArray
        ? (value as string[]).join("\n")
        : JSON.stringify(value, null, 2);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(asText);
  const label = SECTION_LABEL[name] ?? name;

  const save = () => {
    // 字符串数组段：按行拆回数组（去空行）；字符串段：原样。
    const val = isStringArray ? draft.split("\n").filter((l) => l.trim() !== "") : draft;
    onSave(val);
    setEditing(false);
  };

  return (
    <div className="rounded-md border p-3">
      <div className="mb-1 flex items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
        {isAI && (
          <Badge variant="secondary" className="gap-1">
            <Sparkles className="h-3 w-3" /> AI 草拟
          </Badge>
        )}
        {editable && !editing && (
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto h-6 px-2"
            onClick={() => {
              setDraft(asText);
              setEditing(true);
            }}
          >
            <Pencil className="mr-1 h-3 w-3" /> 编辑
          </Button>
        )}
      </div>
      {editing ? (
        <div className="space-y-2">
          <Textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            rows={isStringArray ? 4 : 3}
            autoFocus
          />
          {isStringArray && (
            <p className="text-xs text-muted-foreground">每行一条。</p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setEditing(false)}>
              取消
            </Button>
            <Button size="sm" disabled={saving} onClick={save}>
              保存
            </Button>
          </div>
        </div>
      ) : (
        <p className="whitespace-pre-wrap text-sm">{asText}</p>
      )}
    </div>
  );
}

/** ActionItemRow 单个改进项 + 状态切换 + 删除。 */
function ActionItemRow({
  ai,
  onUpdate,
  onDelete,
  deleting,
}: {
  ai: ActionItem;
  onUpdate: (body: Partial<ActionItem>) => void;
  onDelete: () => void;
  deleting: boolean;
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
      <Button variant="ghost" size="icon" title="删除" disabled={deleting} onClick={onDelete}>
        <Trash2 className="h-4 w-4" />
      </Button>
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
