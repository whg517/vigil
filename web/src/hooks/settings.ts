/**
 * 设置页 hooks —— 通知规则/模板、抑制规则、RBAC（能力域 3/7/13）。
 * 一个文件聚合 settings 页所需的全部分类 CRUD。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

// —— 通知规则 ——
export const notifRuleQk = { notificationRules: () => ["notification-rules"] as const };
export function useNotificationRules() {
  return useQuery({ queryKey: notifRuleQk.notificationRules(), queryFn: () => api.listNotificationRules() });
}
export function useCreateNotificationRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createNotificationRule>[0]) => api.createNotificationRule(body),
    onSuccess: () => { toast.success("通知规则已创建"); qc.invalidateQueries({ queryKey: ["notification-rules"] }); },
  });
}
export function useUpdateNotificationRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateNotificationRule>[1] }) =>
      api.updateNotificationRule(args.id, args.body),
    onSuccess: () => { toast.success("通知规则已更新"); qc.invalidateQueries({ queryKey: ["notification-rules"] }); },
  });
}
export function useDeleteNotificationRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteNotificationRule(id),
    onSuccess: () => { toast.success("通知规则已删除"); qc.invalidateQueries({ queryKey: ["notification-rules"] }); },
  });
}

// —— 抑制规则 ——
export const suppressionQk = { suppressionRules: () => ["suppression-rules"] as const };
export function useSuppressionRules() {
  return useQuery({ queryKey: suppressionQk.suppressionRules(), queryFn: () => api.listSuppressionRules() });
}
export function useCreateSuppressionRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createSuppressionRule>[0]) => api.createSuppressionRule(body),
    onSuccess: () => { toast.success("抑制规则已创建"); qc.invalidateQueries({ queryKey: ["suppression-rules"] }); },
  });
}
export function useUpdateSuppressionRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateSuppressionRule>[1] }) =>
      api.updateSuppressionRule(args.id, args.body),
    onSuccess: () => { toast.success("抑制规则已更新"); qc.invalidateQueries({ queryKey: ["suppression-rules"] }); },
  });
}
export function useDeleteSuppressionRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteSuppressionRule(id),
    onSuccess: () => { toast.success("抑制规则已删除"); qc.invalidateQueries({ queryKey: ["suppression-rules"] }); },
  });
}

// —— 通知模板 ——
export const templateQk = { templates: () => ["notification-templates"] as const };
export function useNotificationTemplates() {
  return useQuery({ queryKey: templateQk.templates(), queryFn: () => api.listNotificationTemplates() });
}
export function useCreateNotificationTemplate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createNotificationTemplate>[0]) => api.createNotificationTemplate(body),
    onSuccess: () => { toast.success("模板已创建"); qc.invalidateQueries({ queryKey: ["notification-templates"] }); },
  });
}
export function useUpdateNotificationTemplate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateNotificationTemplate>[1] }) =>
      api.updateNotificationTemplate(args.id, args.body),
    onSuccess: () => { toast.success("模板已更新"); qc.invalidateQueries({ queryKey: ["notification-templates"] }); },
  });
}
export function useDeleteNotificationTemplate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteNotificationTemplate(id),
    onSuccess: () => { toast.success("模板已删除"); qc.invalidateQueries({ queryKey: ["notification-templates"] }); },
  });
}

// —— RBAC（角色 + 绑定）——
export const rbacQk = { roles: () => ["roles"] as const, bindings: () => ["role-bindings"] as const };
export function useRoles() {
  return useQuery({ queryKey: rbacQk.roles(), queryFn: () => api.listRoles() });
}
export function useCreateRole() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createRole>[0]) => api.createRole(body),
    onSuccess: () => { toast.success("角色已创建"); qc.invalidateQueries({ queryKey: ["roles"] }); },
  });
}
export function useDeleteRole() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteRole(id),
    onSuccess: () => { toast.success("角色已删除"); qc.invalidateQueries({ queryKey: ["roles"] }); },
  });
}
export function useRoleBindings() {
  return useQuery({ queryKey: rbacQk.bindings(), queryFn: () => api.listRoleBindings() });
}
export function useCreateRoleBinding() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createRoleBinding>[0]) => api.createRoleBinding(body),
    onSuccess: () => { toast.success("授权已创建"); qc.invalidateQueries({ queryKey: ["role-bindings"] }); },
  });
}
export function useDeleteRoleBinding() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteRoleBinding(id),
    onSuccess: () => { toast.success("授权已删除"); qc.invalidateQueries({ queryKey: ["role-bindings"] }); },
  });
}
