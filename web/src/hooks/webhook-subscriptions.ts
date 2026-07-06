/**
 * 出站 webhook 订阅 hooks（能力域 1，N2.2）。
 * 仿 integrations.ts 模式：queryKey 对象 + useQuery/useMutation + invalidate。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const webhookSubQk = {
  subscriptions: () => ["webhook-subscriptions"] as const,
};

export function useWebhookSubscriptions() {
  return useQuery({
    queryKey: webhookSubQk.subscriptions(),
    queryFn: () => api.listWebhookSubscriptions(),
  });
}

export function useCreateWebhookSubscription() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createWebhookSubscription>[0]) =>
      api.createWebhookSubscription(body),
    onSuccess: () => {
      toast.success("订阅已创建");
      qc.invalidateQueries({ queryKey: webhookSubQk.subscriptions() });
    },
  });
}

export function useUpdateWebhookSubscription() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateWebhookSubscription>[1] }) =>
      api.updateWebhookSubscription(args.id, args.body),
    onSuccess: () => {
      toast.success("订阅已更新");
      qc.invalidateQueries({ queryKey: webhookSubQk.subscriptions() });
    },
  });
}

export function useDeleteWebhookSubscription() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteWebhookSubscription(id),
    onSuccess: () => {
      toast.success("订阅已删除");
      qc.invalidateQueries({ queryKey: webhookSubQk.subscriptions() });
    },
  });
}
