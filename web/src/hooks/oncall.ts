/**
 * 值班排班 hooks（能力域 5）。
 * 排班列表 + 在班人查询 + 预览。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const oncallQk = {
  schedules: () => ["schedules"] as const,
  schedule: (id: number) => ["schedule", id] as const,
  oncall: (id: number, time?: string) => ["oncall", id, time] as const,
  preview: (id: number, days: number) => ["preview", id, days] as const,
  overrides: (id: number) => ["schedule-overrides", id] as const,
};

/** useSchedules 排班列表。 */
export function useSchedules() {
  return useQuery({ queryKey: oncallQk.schedules(), queryFn: () => api.listSchedules() });
}

/**
 * useSchedule 排班详情（scheduleDetailView，含每层 participants + 轮值配置）。
 * 专供编辑回填：列表的 Schedule 无 participants，PATCH 时若不带全会清空 Rotation。
 */
export function useSchedule(id: number | undefined) {
  return useQuery({
    queryKey: oncallQk.schedule(id ?? 0),
    queryFn: () => api.getSchedule(id!),
    enabled: !!id,
  });
}

/** useOncall 某排班当前在班人。 */
export function useOncall(id: number, time?: string) {
  return useQuery({
    queryKey: oncallQk.oncall(id, time),
    queryFn: () => api.getOncall(id, time),
    enabled: !!id,
  });
}

/** useSchedulePreview 预览未来 N 天排班。 */
export function useSchedulePreview(id: number, days = 14) {
  return useQuery({
    queryKey: oncallQk.preview(id, days),
    queryFn: () => api.previewSchedule(id, days),
    enabled: !!id,
  });
}

/** useCreateSchedule 创建排班。 */
export function useCreateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createSchedule>[0]) => api.createSchedule(body),
    onSuccess: () => {
      toast.success("排班已创建");
      qc.invalidateQueries({ queryKey: ["schedules"] });
      // 新排班可能立即产生在班人：刷新在班/预览缓存
      qc.invalidateQueries({ queryKey: ["oncall"] });
      qc.invalidateQueries({ queryKey: ["preview"] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "创建失败";
      toast.error(`排班创建失败：${msg}`);
    },
  });
}

/** useUpdateSchedule 更新排班（名称/时区/分层）。 */
export function useUpdateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateSchedule>[1] }) =>
      api.updateSchedule(args.id, args.body),
    onSuccess: (_data, args) => {
      toast.success("排班已更新");
      qc.invalidateQueries({ queryKey: ["schedules"] });
      qc.invalidateQueries({ queryKey: oncallQk.schedule(args.id) });
      // 改层/参与人会重建 Rotation，实时在班人随之变：刷新在班/预览缓存
      qc.invalidateQueries({ queryKey: ["oncall", args.id] });
      qc.invalidateQueries({ queryKey: ["preview", args.id] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "更新失败";
      toast.error(`排班更新失败：${msg}`);
    },
  });
}

/** useDeleteSchedule 删除排班。 */
export function useDeleteSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteSchedule(id),
    onSuccess: () => {
      toast.success("排班已删除");
      qc.invalidateQueries({ queryKey: ["schedules"] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "删除失败";
      toast.error(`排班删除失败：${msg}`);
    },
  });
}

// —— 换班 Override（能力域 5，POST/GET/DELETE /schedules/:id/overrides）——

/** useScheduleOverrides 某排班的换班列表。 */
export function useScheduleOverrides(scheduleId: number) {
  return useQuery({
    queryKey: oncallQk.overrides(scheduleId),
    queryFn: () => api.listScheduleOverrides(scheduleId),
    enabled: !!scheduleId,
  });
}

/** useCreateScheduleOverride 创建换班（本人换自己班或 admin 指派他人，权限由后端 403 兜底）。 */
export function useCreateScheduleOverride(scheduleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createScheduleOverride>[1]) =>
      api.createScheduleOverride(scheduleId, body),
    onSuccess: () => {
      toast.success("换班已创建");
      qc.invalidateQueries({ queryKey: oncallQk.overrides(scheduleId) });
      // 换班改变实时在班人：刷新在班/预览缓存
      qc.invalidateQueries({ queryKey: ["oncall", scheduleId] });
      qc.invalidateQueries({ queryKey: ["preview", scheduleId] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "创建失败";
      toast.error(`换班创建失败：${msg}`);
    },
  });
}

/** useDeleteScheduleOverride 删除换班。 */
export function useDeleteScheduleOverride(scheduleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (overrideId: number) => api.deleteScheduleOverride(scheduleId, overrideId),
    onSuccess: () => {
      toast.success("换班已删除");
      qc.invalidateQueries({ queryKey: oncallQk.overrides(scheduleId) });
      qc.invalidateQueries({ queryKey: ["oncall", scheduleId] });
      qc.invalidateQueries({ queryKey: ["preview", scheduleId] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "删除失败";
      toast.error(`换班删除失败：${msg}`);
    },
  });
}
