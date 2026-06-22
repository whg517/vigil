/**
 * EscalationPolicy 升级策略 hooks（能力域 6）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const escalationQk = {
  policies: () => ["escalation-policies"] as const,
};

export function useEscalationPolicies() {
  return useQuery({ queryKey: escalationQk.policies(), queryFn: () => api.listEscalationPolicies() });
}

export function useCreateEscalationPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createEscalationPolicy>[0]) => api.createEscalationPolicy(body),
    onSuccess: () => { toast.success("升级策略已创建"); qc.invalidateQueries({ queryKey: ["escalation-policies"] }); },
  });
}

export function useDeleteEscalationPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteEscalationPolicy(id),
    onSuccess: () => { toast.success("升级策略已删除"); qc.invalidateQueries({ queryKey: ["escalation-policies"] }); },
  });
}
