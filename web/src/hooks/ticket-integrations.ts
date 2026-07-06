/**
 * TicketIntegration 工单集成 hooks（能力域 4 T4.3）。
 * 仿 integrations.ts 模式：queryKey 对象 + useQuery/useMutation + invalidate。
 * 凭据（credential/callback_secret）仅入不出：list 不回显，create/update 收明文加密落库。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const ticketIntegrationQk = {
  ticketIntegrations: () => ["ticket-integrations"] as const,
};

export function useTicketIntegrations() {
  return useQuery({
    queryKey: ticketIntegrationQk.ticketIntegrations(),
    queryFn: () => api.listTicketIntegrations(),
  });
}

export function useCreateTicketIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createTicketIntegration>[0]) => api.createTicketIntegration(body),
    onSuccess: () => {
      toast.success("工单集成已创建");
      qc.invalidateQueries({ queryKey: ["ticket-integrations"] });
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : "创建失败";
      toast.error(`工单集成创建失败：${msg}`);
    },
  });
}

export function useUpdateTicketIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateTicketIntegration>[1] }) =>
      api.updateTicketIntegration(args.id, args.body),
    onSuccess: () => {
      toast.success("工单集成已更新");
      qc.invalidateQueries({ queryKey: ["ticket-integrations"] });
    },
  });
}

export function useDeleteTicketIntegration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteTicketIntegration(id),
    onSuccess: () => {
      toast.success("工单集成已删除");
      qc.invalidateQueries({ queryKey: ["ticket-integrations"] });
    },
  });
}
