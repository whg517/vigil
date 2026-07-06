/**
 * Subscription 个人订阅 hooks（能力域 4/7 T4.4）。
 * 均作用于「当前登录用户自己的」订阅：list/create/delete。
 * 仿 services.ts 模式：queryKey 对象 + useQuery/useMutation + invalidate。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const subscriptionQk = {
  subscriptions: () => ["subscriptions"] as const,
};

export function useSubscriptions() {
  return useQuery({ queryKey: subscriptionQk.subscriptions(), queryFn: () => api.listSubscriptions() });
}

export function useCreateSubscription() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createSubscription>[0]) => api.createSubscription(body),
    onSuccess: () => {
      toast.success("订阅已创建");
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "创建失败";
      toast.error(`订阅创建失败：${msg}`);
    },
  });
}

export function useDeleteSubscription() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteSubscription(id),
    onSuccess: () => {
      toast.success("订阅已删除");
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
    },
  });
}
