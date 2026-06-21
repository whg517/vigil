/**
 * 服务目录 hooks（能力域 4/13）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const serviceQk = {
  services: () => ["services"] as const,
  service: (id: number) => ["service", id] as const,
};

export function useServices() {
  return useQuery({ queryKey: serviceQk.services(), queryFn: () => api.listServices() });
}

export function useService(id: number) {
  return useQuery({
    queryKey: serviceQk.service(id),
    queryFn: () => api.getService(id),
    enabled: !!id,
  });
}

export function useCreateService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createService>[0]) => api.createService(body),
    onSuccess: () => {
      toast.success("服务已创建");
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
}

export function useUpdateService(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.updateService>[1]) => api.updateService(id, body),
    onSuccess: () => {
      toast.success("服务已更新");
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: serviceQk.service(id) });
    },
  });
}

export function useDeleteService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteService(id),
    onSuccess: () => {
      toast.success("服务已删除");
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
}
