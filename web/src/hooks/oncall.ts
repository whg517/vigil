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
};

/** useSchedules 排班列表。 */
export function useSchedules() {
  return useQuery({ queryKey: oncallQk.schedules(), queryFn: () => api.listSchedules() });
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
    onSuccess: () => {
      toast.success("排班已更新");
      qc.invalidateQueries({ queryKey: ["schedules"] });
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
