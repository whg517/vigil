/**
 * Postmortems —— 复盘页（能力域 12）。
 * 列表（裸数组）+ 详情（章节 + ActionItems CRUD + 状态流转）+ 从事件起草。
 * 后端：GET /postmortems，GET /postmortems/:id，PATCH /postmortems/:id/transition，
 *       POST /postmortems/:id/action-items，PATCH /action-items/:id，
 *       POST /incidents/:id/postmortem/draft。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
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

// 状态 → i18n key（标签在组件内经 t() 解析，故此处存 key 而非中文字面量）。
const STATUS_LABEL_KEY: Record<PostmortemStatus, string> = {
  draft: "postmortems.statusDraft",
  in_review: "postmortems.statusInReview",
  published: "postmortems.statusPublished",
  archived: "postmortems.statusArchived",
};
const STATUS_VARIANT: Record<PostmortemStatus, "default" | "secondary" | "outline"> = {
  draft: "secondary",
  in_review: "outline",
  published: "default",
  archived: "outline",
};

export function Postmortems() {
  const { t } = useTranslation();
  const { data, isLoading, isError } = usePostmortems();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [drafting, setDrafting] = useState(false);

  if (selected) return <PostmortemDetail id={selected} onBack={() => setSelected(undefined)} />;

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("postmortems.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("postmortems.subtitle")}</p>
        </div>
        <Button onClick={() => setDrafting(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("postmortems.draftFromIncident")}
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
              title={t("postmortems.emptyTitle")}
              description={t("postmortems.emptyDescription")}
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
                    <span className="font-medium">{t("postmortems.postmortemNo", { id: pm.id })}</span>
                    <Badge variant={STATUS_VARIANT[pm.status]}>{t(STATUS_LABEL_KEY[pm.status])}</Badge>
                    <span className="text-xs text-muted-foreground">{t("postmortems.incidentNo", { id: pm.incident?.id })}</span>
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
  const { t } = useTranslation();
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
        <Button variant="ghost" onClick={onBack}><ArrowLeft className="mr-1 h-4 w-4" />{t("postmortems.back")}</Button>
        <EmptyState title={t("postmortems.notFound")} />
      </div>
    );
  }

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="mr-1 h-4 w-4" />{t("postmortems.backToList")}
        </Button>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">{t("postmortems.statusLabel")}</span>
          <Select
            value={pm.status}
            onChange={(e) => transition.mutate(e.target.value as PostmortemStatus)}
            className="w-32"
            disabled={transition.isPending}
          >
            {(Object.keys(STATUS_LABEL_KEY) as PostmortemStatus[]).map((s) => (
              <option key={s} value={s}>{t(STATUS_LABEL_KEY[s])}</option>
            ))}
          </Select>
          <Button
            variant="outline"
            size="sm"
            disabled={del.isPending}
            onClick={() => {
              if (window.confirm(t("postmortems.deleteConfirm", { id: pm.id }))) {
                del.mutate(pm.id, { onSuccess: onBack });
              }
            }}
          >
            <Trash2 className="mr-1 h-4 w-4" /> {t("postmortems.deletePostmortem")}
          </Button>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">{t("postmortems.postmortemNo", { id: pm.id })}</h1>
        <Badge variant={STATUS_VARIANT[pm.status]}>{t(STATUS_LABEL_KEY[pm.status])}</Badge>
        <span className="text-sm text-muted-foreground">{t("postmortems.linkedIncidentNo", { id: pm.incident?.id })}</span>
      </div>

      <SectionsCard id={pm.id} status={pm.status} sections={pm.sections} />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("postmortems.actionItems")}</CardTitle>
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
            <p className="text-sm text-muted-foreground">{t("postmortems.noActionItems")}</p>
          )}
          <form
            className="flex gap-2 pt-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (!newItem) return;
              addAction.mutate({ description: newItem }, { onSuccess: () => setNewItem("") });
            }}
          >
            <Input value={newItem} onChange={(e) => setNewItem(e.target.value)} placeholder={t("postmortems.addActionItemPlaceholder")} />
            <Button type="submit" size="sm" disabled={addAction.isPending || !newItem}>{t("postmortems.add")}</Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// AI_SECTIONS_KEY sections JSON 内保留键：记录仍标记「AI 草拟」的段落名（与后端 engine.go aiSectionsKey 对齐）。
const AI_SECTIONS_KEY = "_ai_sections";
// 段名 → i18n key（覆盖模板段；未列出的段名原样展示）。标签在 SectionRow 内经 t() 解析。
const SECTION_LABEL_KEY: Record<string, string> = {
  summary: "postmortems.sectionSummary",
  impact: "postmortems.sectionImpact",
  timeline: "postmortems.sectionTimeline",
  root_cause: "postmortems.sectionRootCause",
  contributing_factors: "postmortems.sectionContributingFactors",
  what_went_well: "postmortems.sectionWhatWentWell",
  what_went_wrong: "postmortems.sectionWhatWentWrong",
  action_items: "postmortems.sectionActionItems",
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
  const { t } = useTranslation();
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
          <FileText className="h-4 w-4" /> {t("postmortems.sections")}
          {locked && <Badge variant="outline">{t("postmortems.lockedReadonly")}</Badge>}
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
  const { t } = useTranslation();
  const isStringArray = Array.isArray(value) && value.every((x) => typeof x === "string");
  const asText =
    typeof value === "string"
      ? value
      : isStringArray
        ? (value as string[]).join("\n")
        : JSON.stringify(value, null, 2);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(asText);
  const labelKey = SECTION_LABEL_KEY[name];
  const label = labelKey ? t(labelKey) : name;

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
            <Sparkles className="h-3 w-3" /> {t("postmortems.aiDrafted")}
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
            <Pencil className="mr-1 h-3 w-3" /> {t("common.edit")}
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
            <p className="text-xs text-muted-foreground">{t("postmortems.onePerLine")}</p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setEditing(false)}>
              {t("common.cancel")}
            </Button>
            <Button size="sm" disabled={saving} onClick={save}>
              {t("common.save")}
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
  const { t } = useTranslation();
  const STATUS: ActionItem["status"][] = ["open", "in_progress", "done"];
  // 改进项状态 → i18n key。
  const AI_STATUS_KEY: Record<ActionItem["status"], string> = {
    open: "postmortems.aiStatusOpen",
    in_progress: "postmortems.aiStatusInProgress",
    done: "postmortems.aiStatusDone",
  };
  return (
    <div className="flex items-center gap-2 rounded-md border p-2">
      <span className="flex-1 text-sm">{ai.description}</span>
      <Select
        value={ai.status}
        onChange={(e) => onUpdate({ status: e.target.value as ActionItem["status"] })}
        className="w-32"
      >
        {STATUS.map((s) => (
          <option key={s} value={s}>{t(AI_STATUS_KEY[s])}</option>
        ))}
      </Select>
      {ai.tracker_url && (
        <a href={ai.tracker_url} target="_blank" rel="noreferrer" className="text-xs text-primary hover:underline">
          {t("postmortems.ticket")}
        </a>
      )}
      <Button variant="ghost" size="icon" title={t("common.delete")} disabled={deleting} onClick={onDelete}>
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
  const { t } = useTranslation();
  const gen = useGenerateDraft();
  const [incidentId, setIncidentId] = useState("");
  const parsedId = Number(incidentId);
  // 合法性：非空、是数字、且为正整数（0 不是有效事件 ID）
  const valid = incidentId !== "" && Number.isInteger(parsedId) && parsedId > 0;
  const errMsg = gen.error ? extractError(gen.error) : null;

  return (
    <Dialog open onClose={onClose} title={t("postmortems.draftDialogTitle")} description={t("postmortems.draftDialogDescription")}>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!valid) return;
          gen.mutate(parsedId, { onSuccess: (pm) => onCreated(pm.id) });
        }}
      >
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">{t("postmortems.incidentIdLabel")}</span>
          <Input
            type="number"
            min={1}
            value={incidentId}
            onChange={(e) => setIncidentId(e.target.value)}
            placeholder={t("postmortems.incidentIdPlaceholder")}
            required
            autoFocus
          />
        </label>
        <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
          {t("postmortems.incidentIdHint")}
        </p>
        {errMsg && (
          <p className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            {errMsg}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={gen.isPending || !valid}>
            {gen.isPending ? t("postmortems.drafting") : t("postmortems.draft")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
