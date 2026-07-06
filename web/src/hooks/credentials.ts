/**
 * Credential 凭据托管 hooks（能力域 6，Runbook/工单执行器凭据）。
 * 仿 services.ts 模式：queryKey 对象 + useQuery/useMutation + invalidate。
 * 密文（secret）仅创建/更新时入站加密落库，list 只返元数据（永不回显）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const credentialQk = {
  credentials: () => ["credentials"] as const,
};

export function useCredentials() {
  return useQuery({ queryKey: credentialQk.credentials(), queryFn: () => api.listCredentials() });
}

export function useCreateCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createCredential>[0]) => api.createCredential(body),
    onSuccess: () => {
      toast.success("凭据已创建");
      qc.invalidateQueries({ queryKey: ["credentials"] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "创建失败";
      toast.error(`凭据创建失败：${msg}`);
    },
  });
}

export function useUpdateCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateCredential>[1] }) =>
      api.updateCredential(args.id, args.body),
    onSuccess: () => {
      toast.success("凭据已更新");
      qc.invalidateQueries({ queryKey: ["credentials"] });
    },
  });
}

export function useDeleteCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteCredential(id),
    onSuccess: () => {
      toast.success("凭据已删除");
      qc.invalidateQueries({ queryKey: ["credentials"] });
    },
  });
}
