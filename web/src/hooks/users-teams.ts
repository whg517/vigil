/**
 * User / Team hooks（能力域 13 用户/团队管理）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

// === User ===
export const userQk = { users: () => ["users"] as const };
export function useUsers() {
  return useQuery({ queryKey: userQk.users(), queryFn: () => api.listUsers() });
}
export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Parameters<typeof api.updateUser>[1] }) => api.updateUser(id, body),
    onSuccess: () => { toast.success("用户已更新"); qc.invalidateQueries({ queryKey: ["users"] }); },
  });
}

// === Team ===
export const teamQk = { teams: () => ["teams"] as const };
export function useTeams() {
  return useQuery({ queryKey: teamQk.teams(), queryFn: () => api.listTeams() });
}
export function useCreateTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof api.createTeam>[0]) => api.createTeam(body),
    onSuccess: () => { toast.success("团队已创建"); qc.invalidateQueries({ queryKey: ["teams"] }); },
  });
}
export function useUpdateTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: Parameters<typeof api.updateTeam>[1] }) => api.updateTeam(args.id, args.body),
    onSuccess: () => { toast.success("团队已更新"); qc.invalidateQueries({ queryKey: ["teams"] }); },
  });
}
export function useDeleteTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteTeam(id),
    onSuccess: () => { toast.success("团队已删除"); qc.invalidateQueries({ queryKey: ["teams"] }); },
  });
}
