/**
 * EscalationPolicy 升级策略 hooks（能力域 6）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const escalationQk = {
  policies: () => ["escalation-policies"] as const,
};

export function useEscalationPolicies(teamId?: number) {
  return useQuery({
    // teamId 入 queryKey：团队默认策略选择器按团队列策略，各团队独立缓存。
    queryKey: [...escalationQk.policies(), teamId ?? "all"],
    queryFn: () => api.listEscalationPolicies(teamId != null ? { team_id: teamId } : undefined),
  });
}

export function useCreateEscalationPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createEscalationPolicy>[0]) => api.createEscalationPolicy(body),
    onSuccess: () => { toast.success("升级策略已创建"); qc.invalidateQueries({ queryKey: ["escalation-policies"] }); },
  });
}

export function useUpdateEscalationPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateEscalationPolicy>[1] }) =>
      api.updateEscalationPolicy(args.id, args.body),
    onSuccess: () => { toast.success("升级策略已更新"); qc.invalidateQueries({ queryKey: ["escalation-policies"] }); },
  });
}

export function useDeleteEscalationPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteEscalationPolicy(id),
    onSuccess: () => { toast.success("升级策略已删除"); qc.invalidateQueries({ queryKey: ["escalation-policies"] }); },
  });
}
