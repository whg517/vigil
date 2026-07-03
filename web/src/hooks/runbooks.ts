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
    // approved 决定写操作是否真正执行；干跑（approved=false）时提示写步骤已跳过。
    onSuccess: (_data, vars) => {
      toast.success(vars.approved ? "执行完成" : "干跑完成：写操作步骤已跳过（未批准）");
    },
  });
}
