/**
 * Runbook hooks（能力域 9）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const runbookQk = {
  runbooks: () => ["runbooks"] as const,
  runbook: (id: number) => ["runbook", id] as const,
};

export function useRunbooks() {
  return useQuery({ queryKey: runbookQk.runbooks(), queryFn: () => api.listRunbooks() });
}

export function useRunbook(id: number) {
  return useQuery({
    queryKey: runbookQk.runbook(id),
    queryFn: () => api.getRunbook(id),
    enabled: !!id,
  });
}

export function useCreateRunbook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createRunbook>[0]) => api.createRunbook(body),
    onSuccess: () => {
      toast.success("Runbook 已创建");
      qc.invalidateQueries({ queryKey: ["runbooks"] });
    },
  });
}

export function useUpdateRunbook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateRunbook>[1] }) =>
      api.updateRunbook(args.id, args.body),
    onSuccess: () => {
      toast.success("Runbook 已更新");
      qc.invalidateQueries({ queryKey: ["runbooks"] });
    },
  });
}

export function useDeleteRunbook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteRunbook(id),
    onSuccess: () => {
      toast.success("Runbook 已删除");
      qc.invalidateQueries({ queryKey: ["runbooks"] });
    },
  });
}

/** useExecuteRunbook 执行 Runbook（写操作 human-in-the-loop，approved 确认）。 */
export function useExecuteRunbook() {
  return useMutation({
    mutationFn: (args: { id: number; incidentId: number; approved: boolean }) =>
      api.executeRunbook(args.id, { incident_id: args.incidentId, approved: args.approved }),
    // 依据后端结构化结果给出精确提示：中止 > 写步骤被阻断待审批 > 正常完成。
    // 每步成败/输出由调用方读 mutation.data 逐步渲染（见 runbooks.tsx）。
    onSuccess: (data) => {
      if (data.aborted) {
        toast.error(`执行中止：${data.reason ?? "未知原因"}`);
      } else if (data.pending_approval) {
        toast.warning("部分写步骤未获批准被阻断（human-in-the-loop 闸门生效）");
      } else {
        toast.success("执行完成");
      }
    },
  });
}
