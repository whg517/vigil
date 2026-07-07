/**
 * Oncall —— 值班排班页（能力域 5）。
 * 枚举排班 → 选一个 → 当前在班人 + 未来 N 天预览。
 * 管理能力：创建 / 编辑（名称·类型·时区·分层）/ 删除。
 * 后端：GET /schedules，POST/PATCH/DELETE /schedules/:id，GET /schedules/:id/oncall，GET /schedules/:id/preview。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeftRight, CalendarDays, Pencil, Plus, Trash2, Users } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateSchedule,
  useCreateScheduleOverride,
  useDeleteSchedule,
  useDeleteScheduleOverride,
  useOncall,
  useSchedulePreview,
  useScheduleOverrides,
  useSchedules,
  useUpdateSchedule,
} from "@/hooks/oncall";
import { useUsers } from "@/hooks/users-teams";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { Schedule, ScheduleLayer } from "@/lib/types";

export function Oncall() {
  const { t } = useTranslation();
  const { data: schedules, isLoading: loadingSchedules } = useSchedules();
  const del = useDeleteSchedule();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [days, setDays] = useState(14);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Schedule | undefined>(undefined);

  const id = selected ?? schedules?.[0]?.id ?? 0;
  const oncall = useOncall(id);
  const preview = useSchedulePreview(id, days);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("oncall.title")}</h1>
          <p className="text-sm text-muted-foreground">
            {t("oncall.subtitle")}
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("oncall.createSchedule")}
        </Button>
      </div>

      {loadingSchedules ? (
        <Skeleton className="h-9 w-64" />
      ) : !schedules || schedules.length === 0 ? (
        <Card>
          <CardContent className="p-6">
            <EmptyState
              icon={<CalendarDays className="h-8 w-8" />}
              title={t("oncall.emptyTitle")}
              description={t("oncall.emptyDesc")}
            />
          </CardContent>
        </Card>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={String(id)}
            onChange={(e) => setSelected(Number(e.target.value))}
            className="w-64"
          >
            {schedules.map((s) => (
              <option key={s.id} value={s.id}>
                {t("oncall.scheduleOption", { name: s.name, tz: s.timezone })}
              </option>
            ))}
          </Select>
          <span className="text-xs text-muted-foreground">{t("oncall.previewDays")}</span>
          <Select
            value={String(days)}
            onChange={(e) => setDays(Number(e.target.value))}
            className="w-24"
          >
            {[7, 14, 30, 60].map((d) => (
              <option key={d} value={d}>
                {t("oncall.daysUnit", { n: d })}
              </option>
            ))}
          </Select>
          {id > 0 && (
            <>
              <Button
                size="sm"
                variant="outline"
                onClick={() =>
                  setEditing(schedules.find((s) => s.id === id))
                }
              >
                <Pencil className="mr-1 h-4 w-4" /> {t("common.edit")}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={del.isPending}
                onClick={() => {
                  if (
                    confirm(
                      t("oncall.deleteScheduleConfirm", {
                        name: schedules.find((s) => s.id === id)?.name,
                      }),
                    )
                  ) {
                    del.mutate(id, {
                      onSuccess: () => setSelected(undefined),
                    });
                  }
                }}
              >
                <Trash2 className="mr-1 h-4 w-4" /> {t("common.delete")}
              </Button>
            </>
          )}
        </div>
      )}

      {id > 0 && (
        <div className="grid gap-4 md:grid-cols-2">
          {/* 当前在班人 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <Users className="h-4 w-4" /> {t("oncall.currentOncall")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              {oncall.isLoading ? (
                <Skeleton className="h-16 w-full" />
              ) : oncall.isError || !oncall.data?.layers?.length ? (
                <p className="text-sm text-muted-foreground">{t("oncall.noOncall")}</p>
              ) : (
                <div className="space-y-3">
                  {oncall.data.layers
                    .slice()
                    .sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0))
                    .map((layer) => (
                      <div key={layer.name}>
                        <div className="text-xs font-medium text-muted-foreground">
                          {t("oncall.layerWithPriority", {
                            name: layer.name,
                            priority: layer.priority,
                          })}
                        </div>
                        <div className="mt-1 flex flex-wrap gap-2">
                          {(layer.users ?? []).length === 0 ? (
                            <span className="text-xs text-muted-foreground">—</span>
                          ) : (
                            (layer.users ?? []).map((u) => (
                              <span
                                key={u.id}
                                className="rounded-md bg-primary/10 px-2 py-1 text-sm font-medium text-primary"
                              >
                                {u.name}
                              </span>
                            ))
                          )}
                        </div>
                      </div>
                    ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* 预览 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <CalendarDays className="h-4 w-4" /> {t("oncall.previewTitle", { n: days })}
              </CardTitle>
            </CardHeader>
            <CardContent>
              {preview.isLoading ? (
                <Skeleton className="h-40 w-full" />
              ) : preview.isError || !preview.data?.days?.length ? (
                <p className="text-sm text-muted-foreground">{t("oncall.noPreview")}</p>
              ) : (
                <div className="max-h-72 space-y-1 overflow-auto pr-1">
                  {preview.data.days.map((day) => {
                    const users = (day.layers ?? [])
                      .flatMap((l) => l.users ?? [])
                      .map((u) => u.name)
                      .filter(Boolean);
                    return (
                      <div
                        key={day.date}
                        className={cn(
                          "flex items-center justify-between rounded-md px-2 py-1.5 text-sm",
                          day.date && isToday(day.date) && "bg-primary/10",
                        )}
                      >
                        <span className="font-mono text-xs text-muted-foreground">{day.date}</span>
                        <span className="text-xs">
                          {users.join(t("oncall.userSeparator")) || "—"}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}

      {/* 换班 Override（能力域 5）：临时顶替某段值班，改变实时在班人 */}
      {id > 0 && <OverrideSection scheduleId={id} />}

      {creating && <ScheduleFormDialog onClose={() => setCreating(false)} />}
      {editing && (
        <ScheduleFormDialog schedule={editing} onClose={() => setEditing(undefined)} />
      )}
    </div>
  );
}

/** ScheduleFormDialog 创建/编辑排班。schedule 传则编辑，不传则创建。 */
function ScheduleFormDialog({
  schedule,
  onClose,
}: {
  schedule?: Schedule;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const create = useCreateSchedule();
  const update = useUpdateSchedule();
  const isEdit = !!schedule;

  const [name, setName] = useState(schedule?.name ?? "");
  const [type, setType] = useState<string>(schedule?.type ?? "rotation");
  const [timezone, setTimezone] = useState(schedule?.timezone ?? "Asia/Shanghai");
  const [layers, setLayers] = useState<ScheduleLayer[]>(schedule?.layers ?? []);

  const addLayer = () =>
    setLayers((prev) => [
      ...prev,
      {
        id: `l${Date.now()}`,
        name: prev.length === 0 ? t("oncall.firstLayerName") : `L${prev.length + 1}`,
        priority: prev.length + 1,
        rotation_id: "",
      },
    ]);

  const patchLayer = (idx: number, patch: Partial<ScheduleLayer>) =>
    setLayers((prev) => prev.map((l, i) => (i === idx ? { ...l, ...patch } : l)));

  const removeLayer = (idx: number) =>
    setLayers((prev) => prev.filter((_, i) => i !== idx));

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (isEdit && schedule) {
      update.mutate(
        { id: schedule.id, body: { name, type: type as Schedule["type"], timezone, layers } },
        { onSuccess: onClose },
      );
    } else {
      create.mutate(
        { name, type: type as Schedule["type"], timezone, layers },
        { onSuccess: onClose },
      );
    }
  };

  const pending = create.isPending || update.isPending;

  return (
    <Dialog
      open
      onClose={onClose}
      title={isEdit ? t("oncall.editSchedule") : t("oncall.createSchedule")}
      description={t("oncall.formDesc")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("oncall.nameLabel")}</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("oncall.namePlaceholder")}
            required
            autoFocus
          />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("oncall.typeLabel")}</label>
            <Select value={type} onChange={(e) => setType(e.target.value)}>
              <option value="rotation">{t("oncall.typeRotation")}</option>
              <option value="calendar">{t("oncall.typeCalendar")}</option>
              <option value="follow_the_sun">{t("oncall.typeFollowTheSun")}</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("oncall.timezoneLabel")}</label>
            <Input
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              placeholder="Asia/Shanghai"
            />
          </div>
        </div>

        {/* 分层管理 */}
        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium">{t("oncall.layersLabel")}</label>
            <Button type="button" size="sm" variant="outline" onClick={addLayer}>
              <Plus className="mr-1 h-3.5 w-3.5" /> {t("oncall.addLayer")}
            </Button>
          </div>
          {layers.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              {t("oncall.noLayersHint")}
            </p>
          ) : (
            <div className="space-y-2">
              {layers.map((l, i) => (
                <div key={l.id} className="flex items-center gap-2">
                  <Input
                    value={l.name}
                    onChange={(e) => patchLayer(i, { name: e.target.value })}
                    placeholder={t("oncall.firstLayerName")}
                    className="flex-1"
                  />
                  <Input
                    type="number"
                    min={1}
                    value={l.priority}
                    onChange={(e) => patchLayer(i, { priority: Number(e.target.value) || 1 })}
                    className="w-20"
                    title={t("oncall.priorityHint")}
                  />
                  <Input
                    value={l.rotation_id}
                    onChange={(e) => patchLayer(i, { rotation_id: e.target.value })}
                    placeholder="rotation_id"
                    className="w-32"
                  />
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    onClick={() => removeLayer(i)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 类型徽标预览 */}
        {isEdit && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">{t(`oncall.typeLabel_${type}`, { defaultValue: type })}</Badge>
            <span>· {t("oncall.layerCount", { n: layers.length })}</span>
          </div>
        )}

        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={pending || !name}>
            {pending ? t("common.submitting") : isEdit ? t("common.save") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * OverrideSection 换班 Override 区（能力域 5）。
 * 列出某排班的换班记录 + 新建/删除。本人可换自己班，admin 可指派他人（权限由后端 403 兜底）。
 */
function OverrideSection({ scheduleId }: { scheduleId: number }) {
  const { t } = useTranslation();
  const { data, isLoading } = useScheduleOverrides(scheduleId);
  const del = useDeleteScheduleOverride(scheduleId);
  const [creating, setCreating] = useState(false);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <ArrowLeftRight className="h-4 w-4" /> {t("oncall.overrideSection")}
        </CardTitle>
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("oncall.newOverride")}
        </Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState
            icon={<ArrowLeftRight className="h-8 w-8" />}
            title={t("oncall.overrideEmptyTitle")}
            description={t("oncall.overrideEmptyDesc")}
          />
        ) : (
          <div className="space-y-2">
            {data.map((o) => (
              <div
                key={o.id}
                className="flex items-center justify-between rounded-md border p-2 text-sm"
              >
                <div className="space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">
                      {o.user_name || t("oncall.userFallback", { id: o.user_id })}
                    </span>
                    <Badge variant="secondary" className="text-xs">{t("oncall.overrideBadge")}</Badge>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {formatTime(o.start_time)} → {formatTime(o.end_time)}
                    {o.reason && <span> · {o.reason}</span>}
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  title={t("oncall.deleteOverride")}
                  disabled={del.isPending}
                  onClick={() => {
                    if (confirm(t("oncall.deleteOverrideConfirm"))) {
                      del.mutate(o.id);
                    }
                  }}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </CardContent>
      {creating && (
        <CreateOverrideDialog scheduleId={scheduleId} onClose={() => setCreating(false)} />
      )}
    </Card>
  );
}

/** CreateOverrideDialog 新建换班（替班人 + 起止时间 + 原因）。 */
function CreateOverrideDialog({
  scheduleId,
  onClose,
}: {
  scheduleId: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const create = useCreateScheduleOverride(scheduleId);
  const { data: users } = useUsers();
  const [userId, setUserId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [reason, setReason] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // datetime-local 无时区，转 RFC3339（后端按本地时区解析：附加浏览器时区偏移）。
    create.mutate(
      {
        user_id: Number(userId),
        start_time: toRFC3339(start),
        end_time: toRFC3339(end),
        reason: reason || undefined,
      },
      { onSuccess: onClose },
    );
  };

  const invalid = !userId || !start || !end || (!!start && !!end && end <= start);

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("oncall.newOverrideTitle")}
      description={t("oncall.newOverrideDesc")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("oncall.substituteLabel")}</label>
          <Select value={userId} onChange={(e) => setUserId(e.target.value)} required>
            <option value="" disabled>
              {t("oncall.substitutePlaceholder")}
            </option>
            {(users ?? []).map((u) => (
              <option key={u.id} value={u.id}>
                {u.name}
              </option>
            ))}
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("oncall.startTimeLabel")}</label>
            <Input
              type="datetime-local"
              value={start}
              onChange={(e) => setStart(e.target.value)}
              required
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("oncall.endTimeLabel")}</label>
            <Input
              type="datetime-local"
              value={end}
              onChange={(e) => setEnd(e.target.value)}
              required
            />
          </div>
        </div>
        {!!start && !!end && end <= start && (
          <p className="text-xs text-destructive">{t("oncall.endAfterStart")}</p>
        )}
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("oncall.reasonLabel")}</label>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("oncall.reasonPlaceholder")}
          />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={create.isPending || invalid}>
            {create.isPending ? t("oncall.creatingOverride") : t("oncall.createOverride")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** toRFC3339 把 datetime-local 值（无时区）转为带本地时区偏移的 RFC3339。 */
function toRFC3339(local: string): string {
  // local 形如 "2026-07-06T14:30"；new Date 按本地时区解析，toISOString 输出 UTC（后端可解析）。
  return new Date(local).toISOString();
}

/** isToday 判断日期串（YYYY-MM-DD 或 RFC3339）是否今天。 */
function isToday(s: string): boolean {
  const today = new Date().toISOString().slice(0, 10);
  return s.startsWith(today);
}
