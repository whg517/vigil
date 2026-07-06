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
  configTemplates: () => ["integration-config-templates"] as const,
};

export function useIntegrations() {
  return useQuery({ queryKey: integrationQk.integrations(), queryFn: () => api.listIntegrations() });
}

/** useConfigTemplates 集成向导 step1：全部接入类型的配置模板/简介（列出可选源）。静态说明数据，长缓存。 */
export function useConfigTemplates() {
  return useQuery({
    queryKey: integrationQk.configTemplates(),
    queryFn: () => api.listConfigTemplates(),
    staleTime: 5 * 60 * 1000,
  });
}

/** useTestIntegration 向导 step4：干跑测试接入点（归一化预览，不建单）。 */
export function useTestIntegration() {
  return useMutation({
    mutationFn: (args: { id: number; payload: unknown }) => api.testIntegration(args.id, args.payload),
  });
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

/** useUpdateIntegration 更新接入点（改名/启停）。 */
export function useUpdateIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateIntegration>[1] }) =>
      api.updateIntegration(args.id, args.body),
    onSuccess: () => { toast.success("接入点已更新"); qc.invalidateQueries({ queryKey: ["integrations"] }); },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "更新失败";
      toast.error(`接入点更新失败：${msg}`);
    },
  });
}
