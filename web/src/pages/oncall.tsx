/**
 * Oncall —— 值班排班页（能力域 5）。
 * 枚举排班 → 选一个 → 当前在班人 + 未来 N 天预览。
 * 管理能力：创建 / 编辑（名称·类型·时区·分层）/ 删除。
 * 后端：GET /schedules，POST/PATCH/DELETE /schedules/:id，GET /schedules/:id/oncall，GET /schedules/:id/preview。
 */
import { useState } from "react";
import { CalendarDays, Pencil, Plus, Trash2, Users } from "lucide-react";
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
  useDeleteSchedule,
  useOncall,
  useSchedulePreview,
  useSchedules,
  useUpdateSchedule,
} from "@/hooks/oncall";
import { cn } from "@/lib/utils";
import type { Schedule, ScheduleLayer } from "@/lib/types";

const TYPE_LABEL: Record<string, string> = {
  calendar: "日历",
  rotation: "轮班",
  follow_the_sun: "跟随太阳",
};

export function Oncall() {
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
          <h1 className="text-2xl font-semibold tracking-tight">值班排班</h1>
          <p className="text-sm text-muted-foreground">
            实时回答"此刻谁在班"。排班是蓝图，值班人由引擎实时计算（不存快照）。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建排班
        </Button>
      </div>

      {loadingSchedules ? (
        <Skeleton className="h-9 w-64" />
      ) : !schedules || schedules.length === 0 ? (
        <Card>
          <CardContent className="p-6">
            <EmptyState
              icon={<CalendarDays className="h-8 w-8" />}
              title="还没有排班"
              description="创建排班后，告警升级会按排班实时算出在班人。"
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
                {s.name}（{s.timezone}）
              </option>
            ))}
          </Select>
          <span className="text-xs text-muted-foreground">预览天数</span>
          <Select
            value={String(days)}
            onChange={(e) => setDays(Number(e.target.value))}
            className="w-24"
          >
            {[7, 14, 30, 60].map((d) => (
              <option key={d} value={d}>
                {d} 天
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
                <Pencil className="mr-1 h-4 w-4" /> 编辑
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={del.isPending}
                onClick={() => {
                  if (confirm(`确认删除排班「${schedules.find((s) => s.id === id)?.name}」？`)) {
                    del.mutate(id, {
                      onSuccess: () => setSelected(undefined),
                    });
                  }
                }}
              >
                <Trash2 className="mr-1 h-4 w-4" /> 删除
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
                <Users className="h-4 w-4" /> 当前在班人
              </CardTitle>
            </CardHeader>
            <CardContent>
              {oncall.isLoading ? (
                <Skeleton className="h-16 w-full" />
              ) : oncall.isError || !oncall.data?.layers?.length ? (
                <p className="text-sm text-muted-foreground">无在班人或排班未配置。</p>
              ) : (
                <div className="space-y-3">
                  {oncall.data.layers
                    .slice()
                    .sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0))
                    .map((layer) => (
                      <div key={layer.name}>
                        <div className="text-xs font-medium text-muted-foreground">
                          {layer.name}（优先级 {layer.priority}）
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
                <CalendarDays className="h-4 w-4" /> 未来 {days} 天预览
              </CardTitle>
            </CardHeader>
            <CardContent>
              {preview.isLoading ? (
                <Skeleton className="h-40 w-full" />
              ) : preview.isError || !preview.data?.days?.length ? (
                <p className="text-sm text-muted-foreground">暂无预览数据。</p>
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
                          {users.join("、") || "—"}
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
        name: prev.length === 0 ? "一线" : `L${prev.length + 1}`,
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
      title={isEdit ? "编辑排班" : "创建排班"}
      description="排班是蓝图，分层（primary/secondary/override）决定值班优先级。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="SRE 主排班"
            required
            autoFocus
          />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">类型</label>
            <Select value={type} onChange={(e) => setType(e.target.value)}>
              <option value="rotation">轮班（rotation）</option>
              <option value="calendar">日历（calendar）</option>
              <option value="follow_the_sun">跟随太阳（follow_the_sun）</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">时区</label>
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
            <label className="text-sm font-medium">分层</label>
            <Button type="button" size="sm" variant="outline" onClick={addLayer}>
              <Plus className="mr-1 h-3.5 w-3.5" /> 添加层
            </Button>
          </div>
          {layers.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              未配置分层。创建后可在「在班人」无数据时回来补充（关联轮班规则）。
            </p>
          ) : (
            <div className="space-y-2">
              {layers.map((l, i) => (
                <div key={l.id} className="flex items-center gap-2">
                  <Input
                    value={l.name}
                    onChange={(e) => patchLayer(i, { name: e.target.value })}
                    placeholder="一线"
                    className="flex-1"
                  />
                  <Input
                    type="number"
                    min={1}
                    value={l.priority}
                    onChange={(e) => patchLayer(i, { priority: Number(e.target.value) || 1 })}
                    className="w-20"
                    title="优先级（数字越小越高）"
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
            <Badge variant="outline">{TYPE_LABEL[type] ?? type}</Badge>
            <span>· {layers.length} 个分层</span>
          </div>
        )}

        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={pending || !name}>
            {pending ? "保存中..." : isEdit ? "保存" : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** isToday 判断日期串（YYYY-MM-DD 或 RFC3339）是否今天。 */
function isToday(s: string): boolean {
  const today = new Date().toISOString().slice(0, 10);
  return s.startsWith(today);
}
