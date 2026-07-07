/**
 * 升级策略管理页（能力域 6）。
 * 列出升级策略，创建/编辑时配置 name + repeat_times + 多层级（延迟/通道/目标）。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronUp, Pencil, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateEscalationPolicy,
  useDeleteEscalationPolicy,
  useEscalationPolicies,
  useUpdateEscalationPolicy,
} from "@/hooks/escalation-policies";
import { formatTime } from "@/lib/format";
import type { EscalationPolicy } from "@/lib/types";

const CHANNELS = ["im", "phone", "sms", "email", "webhook"] as const;
type Channel = (typeof CHANNELS)[number];

/**
 * 表单内的升级层级类型（字段全必填，便于受控 state）。
 * target_id 用 string（与后端 schema 的 schedule_id/user_id/team_id 一致）。
 */
interface FormLevel {
  level: number;
  delay_minutes: number;
  targets: { type: string; target_id: string }[];
  notify_channels: string[];
}

export function EscalationPolicies() {
  const { t } = useTranslation();
  const { data, isLoading } = useEscalationPolicies();
  const del = useDeleteEscalationPolicy();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<EscalationPolicy | undefined>(undefined);

  const levelSummary = (levels?: { level?: number; delay_minutes?: number }[]) => {
    if (!levels || levels.length === 0) return "—";
    return levels.map((l) => `L${l.level ?? "?"}·${l.delay_minutes ?? 0}min`).join(" → ");
  };

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">{t("escalationPolicies.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("escalationPolicies.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("escalationPolicies.createPolicy")}
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<ChevronUp className="h-8 w-8" />}
              title={t("escalationPolicies.emptyTitle")}
              description={t("escalationPolicies.emptyDescription")}
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("escalationPolicies.colName")}</th>
                  <th className="p-3">{t("escalationPolicies.colLevels")}</th>
                  <th className="p-3">{t("escalationPolicies.colRepeat")}</th>
                  <th className="p-3">{t("escalationPolicies.colCreatedAt")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((p) => (
                  <tr key={p.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{p.name}</td>
                    <td className="p-3 text-muted-foreground">{levelSummary(p.levels)}</td>
                    <td className="p-3"><Badge variant="secondary">{p.repeat_times}</Badge></td>
                    <td className="p-3 text-muted-foreground">{formatTime(p.created_at)}</td>
                    <td className="p-3 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Button size="icon" variant="ghost" title={t("common.edit")} onClick={() => setEditing(p)}>
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <Button size="icon" variant="ghost" disabled={del.isPending} onClick={() => del.mutate(p.id)}>
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {creating && <EscalationPolicyFormDialog onClose={() => setCreating(false)} />}
      {editing && <EscalationPolicyFormDialog policy={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** 单个升级层级行编辑：level 序号只读，delay_minutes / notify_channels / targets 可改。 */
function LevelRow({
  level,
  onChange,
  onRemove,
}: {
  level: FormLevel;
  onChange: (patch: Partial<FormLevel>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const toggleChannel = (ch: Channel) => {
    const has = level.notify_channels.includes(ch);
    const next = has
      ? level.notify_channels.filter((c) => c !== ch)
      : [...level.notify_channels, ch];
    onChange({ notify_channels: next });
  };

  return (
    <div className="rounded-md border p-2 space-y-2">
      <div className="flex items-center gap-2">
        <Badge variant="outline">L{level.level}</Badge>
        <div className="flex-1" />
        <Button type="button" size="icon" variant="ghost" title={t("escalationPolicies.removeLevel")} onClick={onRemove}>
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">{t("escalationPolicies.delayMinutes")}</label>
          <Input
            type="number"
            min={0}
            value={level.delay_minutes}
            onChange={(e) => onChange({ delay_minutes: Number(e.target.value) || 0 })}
            className="h-8"
          />
        </div>
      </div>
      <div className="space-y-1">
        <label className="text-xs text-muted-foreground">{t("escalationPolicies.notifyChannels")}</label>
        <div className="flex flex-wrap gap-1">
          {CHANNELS.map((ch) => {
            const on = level.notify_channels.includes(ch);
            return (
              <button
                key={ch}
                type="button"
                onClick={() => toggleChannel(ch)}
                className={`rounded border px-2 py-0.5 text-xs transition-colors ${
                  on ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
                }`}
              >
                {ch}
              </button>
            );
          })}
        </div>
      </div>
      <div className="space-y-1">
        <label className="text-xs text-muted-foreground">{t("escalationPolicies.targetsLabel")}</label>
        <Input
          value={level.targets.map((t) => `${t.type}:${t.target_id}`).join(",")}
          onChange={(e) => onChange({ targets: parseTargets(e.target.value) })}
          placeholder="schedule:1, user:3"
          className="h-8 text-xs"
        />
      </div>
    </div>
  );
}

/** 把 "schedule:1, user:3" 解析为 targets 数组（target_id 与后端一致用 string）。 */
function parseTargets(text: string): FormLevel["targets"] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      const [type, idStr] = s.split(":");
      return { type: (type || "").trim(), target_id: (idStr || "").trim() };
    });
}

/**
 * EscalationPolicyFormDialog 创建/编辑复用：传 policy 则编辑。
 * levels 为有序数组，level 序号自动递增；支持增删层。
 */
function EscalationPolicyFormDialog({
  policy,
  onClose,
}: {
  policy?: EscalationPolicy;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const isEdit = !!policy;
  const create = useCreateEscalationPolicy();
  const update = useUpdateEscalationPolicy();

  const [name, setName] = useState(policy?.name ?? "");
  const [repeatTimes, setRepeatTimes] = useState(String(policy?.repeat_times ?? 1));
  const [levels, setLevels] = useState<FormLevel[]>(() =>
    policy?.levels && policy.levels.length > 0
      ? policy.levels.map((l) => ({
          level: l.level ?? 1,
          delay_minutes: l.delay_minutes ?? 5,
          targets: (l.targets ?? []).map((t) => ({
            type: String(t.type ?? ""),
            target_id: String(t.target_id ?? (t as { id?: number }).id ?? ""),
          })),
          notify_channels: [...(l.notify_channels ?? [])],
        }))
      : [{ level: 1, delay_minutes: 5, targets: [], notify_channels: ["im"] }],
  );

  const addLevel = () =>
    setLevels((prev) => [
      ...prev,
      {
        level: prev.length + 1,
        delay_minutes: 5,
        targets: [],
        notify_channels: ["im"],
      },
    ]);

  const patchLevel = (idx: number, patch: Partial<FormLevel>) =>
    setLevels((prev) => prev.map((l, i) => (i === idx ? { ...l, ...patch } : l)));

  const removeLevel = (idx: number) =>
    setLevels((prev) =>
      prev.filter((_, i) => i !== idx).map((l, i) => ({ ...l, level: i + 1 })),
    );

  const pending = create.isPending || update.isPending;

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const body = {
      name,
      repeat_times: Number(repeatTimes) || 1,
      levels: levels.map((l, i) => ({ ...l, level: i + 1 })),
    };
    if (isEdit && policy) {
      update.mutate({ id: policy.id, body }, { onSuccess: onClose });
    } else {
      create.mutate(body, { onSuccess: onClose });
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={
        isEdit
          ? t("escalationPolicies.editTitle", { name: policy?.name })
          : t("escalationPolicies.createTitle")
      }
      description={t("escalationPolicies.formDescription")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("escalationPolicies.nameLabel")}</label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("escalationPolicies.namePlaceholder")}
              required
              autoFocus
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("escalationPolicies.repeatTimesLabel")}</label>
            <Input
              type="number"
              min={1}
              value={repeatTimes}
              onChange={(e) => setRepeatTimes(e.target.value)}
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium">{t("escalationPolicies.levelsLabel")}</label>
            <Button type="button" size="sm" variant="outline" onClick={addLevel}>
              <Plus className="mr-1 h-3.5 w-3.5" /> {t("escalationPolicies.addLevel")}
            </Button>
          </div>
          <div className="space-y-2">
            {levels.map((l, i) => (
              <LevelRow
                key={i}
                level={l}
                onChange={(patch) => patchLevel(i, patch)}
                onRemove={() => removeLevel(i)}
              />
            ))}
            {levels.length === 0 && (
              <p className="text-xs text-muted-foreground">{t("escalationPolicies.noLevels")}</p>
            )}
          </div>
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={pending || !name}>
            {pending ? t("common.submitting") : isEdit ? t("common.save") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
