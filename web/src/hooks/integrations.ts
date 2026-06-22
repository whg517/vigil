/**
 * Integration 接入点 hooks（能力域 1）。
 * 仿 services.ts 模式：queryKey 对象 + useQuery/useMutation + invalidate。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const integrationQk = {
  integrations: () => ["integrations"] as const,
  integration: (id: number) => ["integration", id] as const,
};

export function useIntegrations() {
  return useQuery({ queryKey: integrationQk.integrations(), queryFn: () => api.listIntegrations() });
}

export function useCreateIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createIntegration>[0]) => api.createIntegration(body),
    onSuccess: () => { toast.success("接入点已创建"); qc.invalidateQueries({ queryKey: ["integrations"] }); },
  });
}

export function useDeleteIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteIntegration(id),
    onSuccess: () => { toast.success("接入点已删除"); qc.invalidateQueries({ queryKey: ["integrations"] }); },
  });
}
