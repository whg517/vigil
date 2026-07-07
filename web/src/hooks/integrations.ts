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

/**
 * useIntegration 接入点详情（含 webhook 鉴权 token）。
 * 编辑弹窗打开时按 id 拉取，供持久展示接入 URL/token（列表不回显 token）。
 * enabled 门控：仅在传入有效 id 时发请求。
 */
export function useIntegration(id: number | undefined) {
  return useQuery({
    queryKey: integrationQk.integration(id ?? 0),
    queryFn: () => api.getIntegration(id as number),
    enabled: id != null && id > 0,
  });
}

/**
 * useRotateIntegrationToken 轮换 webhook 鉴权 token（高危：旧 token 立即失效）。
 * 轮换后刷新详情与列表缓存，弹窗展示新 URL/token。
 */
export function useRotateIntegrationToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.rotateIntegrationToken(id),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: integrationQk.integration(id) });
      qc.invalidateQueries({ queryKey: integrationQk.integrations() });
    },
  });
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
