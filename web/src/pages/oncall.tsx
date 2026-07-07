/**
 * Oncall —— 值班排班页（能力域 5）。
 * 枚举排班 → 选一个 → 当前在班人 + 未来 N 天预览。
 * 管理能力：创建 / 编辑（名称·类型·时区·分层）/ 删除。
 * 后端：GET /schedules，POST/PATCH/DELETE /schedules/:id，GET /schedules/:id/oncall，GET /schedules/:id/preview。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeftRight, CalendarDays, Pencil, Plus, Trash2, Users, X } from "lucide-react";
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
  useSchedule,
  useSchedulePreview,
  useScheduleOverrides,
  useSchedules,
  useUpdateSchedule,
} from "@/hooks/oncall";
import { useUsers } from "@/hooks/users-teams";
import { formatTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  CreateScheduleReq,
  Schedule,
  ScheduleDetail,
  ScheduleLayer,
  ScheduleLayerReq,
} from "@/lib/types";

/** 新层默认值（rotation_type=daily / shift=24h / handoff=09:00）。 */
function makeLayer(name: string, priority: number): ScheduleLayer {
  return {
    id: `l${Date.now()}${Math.random().toString(36).slice(2, 6)}`,
    name,
    priority,
    participants: [],
    rotation_type: "daily",
    shift_length: "24h",
    handoff_time: "09:00",
    start_date: "",
  };
}

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

/**
 * ScheduleFormDialog 创建/编辑排班。schedule 传则编辑，不传则创建。
 * 编辑时先 GET /schedules/:id 拉详情（含每层 participants + 轮值配置）回填，
 * 避免 PATCH 带的 layer 缺 participants 导致后端删旧 Rotation 后清空在班人。
 */
function ScheduleFormDialog({
  schedule,
  onClose,
}: {
  schedule?: Schedule;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const isEdit = !!schedule;
  // 编辑：拉详情视图回填（participants + 轮值配置）；新建：无需拉取。
  const detail = useSchedule(isEdit ? schedule!.id : undefined);

  // 详情加载中时展示占位（避免用空表单覆盖已有数据）。
  if (isEdit && detail.isLoading) {
    return (
      <Dialog open onClose={onClose} title={t("oncall.editSchedule")} description={t("oncall.formDesc")}>
        <Skeleton className="h-64 w-full" />
      </Dialog>
    );
  }

  return (
    <ScheduleFormInner
      schedule={schedule}
      detail={isEdit ? detail.data : undefined}
      onClose={onClose}
    />
  );
}

/** detailToLayers 把详情视图的 layerDetailView 转成表单层模型（补默认值）。 */
function detailToLayers(detail: ScheduleDetail): ScheduleLayer[] {
  return (detail.layers ?? []).map((l, i) => ({
    id: `l${i}-${l.rotation_id ?? l.name ?? i}`,
    name: l.name ?? "",
    priority: l.priority ?? i + 1,
    participants: l.participants ?? [],
    rotation_type: l.rotation_type || "daily",
    shift_length: l.shift_length || "24h",
    handoff_time: l.handoff_time || "09:00",
    start_date: l.start_date ?? "",
    timezone: l.timezone,
    work_start: l.work_start,
    work_end: l.work_end,
  }));
}

/** ScheduleFormInner 实际表单（detail 就绪后挂载，用初始值填 state）。 */
function ScheduleFormInner({
  schedule,
  detail,
  onClose,
}: {
  schedule?: Schedule;
  detail?: ScheduleDetail;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const create = useCreateSchedule();
  const update = useUpdateSchedule();
  const { data: users } = useUsers();
  const isEdit = !!schedule;

  const [name, setName] = useState(detail?.name ?? schedule?.name ?? "");
  const [type, setType] = useState<string>(detail?.type ?? schedule?.type ?? "rotation");
  const [timezone, setTimezone] = useState(
    detail?.timezone ?? schedule?.timezone ?? "Asia/Shanghai",
  );
  const [layers, setLayers] = useState<ScheduleLayer[]>(() =>
    detail ? detailToLayers(detail) : [],
  );

  // calendar 全员常驻，轮值/班次/交接/开始日期无意义 → 隐藏；follow_the_sun 额外每层时区/工作时段。
  const showRotationFields = type !== "calendar";
  const showFtsFields = type === "follow_the_sun";

  const addLayer = () =>
    setLayers((prev) => [
      ...prev,
      makeLayer(
        prev.length === 0 ? t("oncall.firstLayerName") : `L${prev.length + 1}`,
        prev.length + 1,
      ),
    ]);

  const patchLayer = (idx: number, patch: Partial<ScheduleLayer>) =>
    setLayers((prev) => prev.map((l, i) => (i === idx ? { ...l, ...patch } : l)));

  const removeLayer = (idx: number) =>
    setLayers((prev) => prev.filter((_, i) => i !== idx));

  const toggleParticipant = (idx: number, userId: number) =>
    setLayers((prev) =>
      prev.map((l, i) => {
        if (i !== idx) return l;
        const has = l.participants.includes(userId);
        return {
          ...l,
          participants: has
            ? l.participants.filter((u) => u !== userId)
            : [...l.participants, userId],
        };
      }),
    );

  // 构造提交层：只带后端认识的字段（id 是前端本地 key，不提交）。
  const toReqLayer = (l: ScheduleLayer): ScheduleLayerReq => {
    const req: ScheduleLayerReq = {
      name: l.name,
      priority: l.priority,
      participants: l.participants,
    };
    if (showRotationFields) {
      req.rotation_type = l.rotation_type;
      req.shift_length = l.shift_length;
      req.handoff_time = l.handoff_time;
      if (l.start_date) req.start_date = toRFC3339(l.start_date);
    }
    if (showFtsFields) {
      if (l.timezone) req.timezone = l.timezone;
      if (l.work_start) req.work_start = l.work_start;
      if (l.work_end) req.work_end = l.work_end;
    }
    return req;
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const reqLayers = layers.map(toReqLayer);
    if (isEdit && schedule) {
      update.mutate(
        { id: schedule.id, body: { name, type, timezone, layers: reqLayers } },
        { onSuccess: onClose },
      );
    } else {
      const body: CreateScheduleReq = { name, type, timezone, layers: reqLayers };
      create.mutate(body, { onSuccess: onClose });
    }
  };

  const pending = create.isPending || update.isPending;
  // 有层但存在层没选人 → 提示（可提交，避免用户再次踩空）。
  const hasEmptyLayer = layers.some((l) => l.participants.length === 0);

  return (
    <Dialog
      open
      onClose={onClose}
      title={isEdit ? t("oncall.editSchedule") : t("oncall.createSchedule")}
      description={t("oncall.formDesc")}
    >
      <form className="max-h-[70vh] space-y-3 overflow-y-auto pr-1" onSubmit={onSubmit}>
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

        {/* 分层管理：每层 = 名称 + 优先级 + 参与人 + 轮值配置 */}
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium">{t("oncall.layersLabel")}</label>
            <Button type="button" size="sm" variant="outline" onClick={addLayer}>
              <Plus className="mr-1 h-3.5 w-3.5" /> {t("oncall.addLayer")}
            </Button>
          </div>
          {layers.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("oncall.noLayersHint")}</p>
          ) : (
            <div className="space-y-3">
              {layers.map((l, i) => (
                <div key={l.id} className="space-y-2 rounded-md border p-3">
                  {/* 名称 + 优先级 + 删除 */}
                  <div className="flex items-center gap-2">
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
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      onClick={() => removeLayer(i)}
                      title={t("oncall.removeLayer")}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>

                  {/* 参与人多选 */}
                  <div className="space-y-1.5">
                    <label className="text-xs font-medium text-muted-foreground">
                      {t("oncall.participants")}
                    </label>
                    {/* 已选 chips */}
                    {l.participants.length > 0 && (
                      <div className="flex flex-wrap gap-1.5">
                        {l.participants.map((uid) => {
                          const u = users?.find((x) => x.id === uid);
                          return (
                            <span
                              key={uid}
                              className="inline-flex items-center gap-1 rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
                            >
                              {u?.name ?? t("oncall.userFallback", { id: uid })}
                              <button
                                type="button"
                                className="text-primary/70 hover:text-primary"
                                onClick={() => toggleParticipant(i, uid)}
                                title={t("oncall.removeParticipant")}
                              >
                                <X className="h-3 w-3" />
                              </button>
                            </span>
                          );
                        })}
                      </div>
                    )}
                    {/* 添加参与人下拉：选中即加入，不显示已选 */}
                    <Select
                      value=""
                      onChange={(e) => {
                        const uid = Number(e.target.value);
                        if (uid) toggleParticipant(i, uid);
                      }}
                    >
                      <option value="">{t("oncall.addParticipant")}</option>
                      {(users ?? [])
                        .filter((u) => !l.participants.includes(u.id))
                        .map((u) => (
                          <option key={u.id} value={u.id}>
                            {u.name}
                          </option>
                        ))}
                    </Select>
                    {l.participants.length === 0 && (
                      <p className="text-xs text-amber-600 dark:text-amber-500">
                        {t("oncall.noParticipantsWarn")}
                      </p>
                    )}
                  </div>

                  {/* 轮值配置（calendar 隐藏） */}
                  {showRotationFields && (
                    <div className="grid grid-cols-2 gap-2">
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.rotationType")}
                        </label>
                        <Select
                          value={l.rotation_type}
                          onChange={(e) => patchLayer(i, { rotation_type: e.target.value })}
                        >
                          <option value="daily">{t("oncall.rotationDaily")}</option>
                          <option value="weekly">{t("oncall.rotationWeekly")}</option>
                          <option value="custom">{t("oncall.rotationCustom")}</option>
                        </Select>
                      </div>
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.shiftLength")}
                        </label>
                        <Input
                          value={l.shift_length}
                          onChange={(e) => patchLayer(i, { shift_length: e.target.value })}
                          placeholder="24h"
                        />
                      </div>
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.handoffTime")}
                        </label>
                        <Input
                          value={l.handoff_time}
                          onChange={(e) => patchLayer(i, { handoff_time: e.target.value })}
                          placeholder="09:00"
                        />
                      </div>
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.startDate")}
                        </label>
                        <Input
                          type="date"
                          value={(l.start_date || "").slice(0, 10)}
                          onChange={(e) => patchLayer(i, { start_date: e.target.value })}
                        />
                      </div>
                    </div>
                  )}

                  {/* follow_the_sun 专用：本层时区 + 工作时段 */}
                  {showFtsFields && (
                    <div className="grid grid-cols-3 gap-2">
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.layerTimezone")}
                        </label>
                        <Input
                          value={l.timezone ?? ""}
                          onChange={(e) => patchLayer(i, { timezone: e.target.value })}
                          placeholder="Asia/Shanghai"
                        />
                      </div>
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.workStart")}
                        </label>
                        <Input
                          value={l.work_start ?? ""}
                          onChange={(e) => patchLayer(i, { work_start: e.target.value })}
                          placeholder="09:00"
                        />
                      </div>
                      <div className="space-y-1">
                        <label className="text-xs text-muted-foreground">
                          {t("oncall.workEnd")}
                        </label>
                        <Input
                          value={l.work_end ?? ""}
                          onChange={(e) => patchLayer(i, { work_end: e.target.value })}
                          placeholder="17:00"
                        />
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
          {layers.length > 0 && hasEmptyLayer && (
            <p className="text-xs text-amber-600 dark:text-amber-500">
              {t("oncall.someLayersEmptyWarn")}
            </p>
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
