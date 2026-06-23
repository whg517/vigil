/**
 * 复盘 hooks（能力域 12）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Postmortem } from "@/lib/types";
import { toast } from "sonner";

export const postmortemQk = {
  postmortems: () => ["postmortems"] as const,
  postmortem: (id: number) => ["postmortem", id] as const,
};

export function usePostmortems() {
  return useQuery({ queryKey: postmortemQk.postmortems(), queryFn: () => api.listPostmortems() });
}

export function usePostmortem(id: number) {
  return useQuery({
    queryKey: postmortemQk.postmortem(id),
    queryFn: () => api.getPostmortem(id),
    enabled: !!id,
  });
}

/** useGenerateDraft 从事件生成复盘草稿。 */
export function useGenerateDraft() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (incidentId: number) => api.generatePostmortemDraft(incidentId),
    onSuccess: (pm) => {
      toast.success(`复盘草稿已生成（#${pm.id}）`);
      qc.invalidateQueries({ queryKey: ["postmortems"] });
    },
  });
}

/** useTransitionPostmortem 状态流转。 */
export function useTransitionPostmortem(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (status: Postmortem["status"]) => api.transitionPostmortem(id, status),
    onSuccess: (data) => {
      toast.success(`状态已更新为 ${data.status}`);
      qc.setQueryData(postmortemQk.postmortem(id), data);
      qc.invalidateQueries({ queryKey: ["postmortems"] });
    },
  });
}

export function useAddActionItem(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { description: string; owner_id?: string }) => api.addActionItem(id, body),
    onSuccess: () => {
      toast.success("改进项已添加");
      qc.invalidateQueries({ queryKey: postmortemQk.postmortem(id) });
    },
  });
}

export function useUpdateActionItem(pmId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateActionItem>[1] }) =>
      api.updateActionItem(args.id, args.body),
    onSuccess: () => {
      toast.success("改进项已更新");
      qc.invalidateQueries({ queryKey: postmortemQk.postmortem(pmId) });
    },
  });
}

/** useDeletePostmortem 删除复盘（后端会级联删除其改进项）。onSuccess 回调用于导航回列表。 */
export function useDeletePostmortem() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deletePostmortem(id),
    onSuccess: () => {
      toast.success("复盘已删除");
      qc.invalidateQueries({ queryKey: ["postmortems"] });
    },
  });
}

/** useDeleteActionItem 删除单个改进项。 */
export function useDeleteActionItem(pmId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteActionItem(id),
    onSuccess: () => {
      toast.success("改进项已删除");
      qc.invalidateQueries({ queryKey: postmortemQk.postmortem(pmId) });
    },
  });
}
