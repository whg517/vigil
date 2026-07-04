/**
 * 登录态 hooks（能力域 13）。
 * useLogin：登录 mutation（成功后 token 由 authApi.login 内部存 localStorage）。
 */
import { useMutation } from "@tanstack/react-query";
import { authApi } from "@/lib/auth";

export function useLogin() {
  return useMutation({
    mutationFn: ({ username, password }: { username: string; password: string }) =>
      authApi.login(username, password),
    // 成功后 token 已在 authApi.login 内存入 localStorage，调用方在 onSuccess 跳转。
  });
}

// useChangePassword：改密 mutation（T0.4）。
// 成功后 authApi.changePassword 内部已清除强制改密标志；调用方在 onSuccess 跳转。
// 注意：改密后后端会吊销当前 token（token_version 自增），旧 access 立即失效——
// 因此调用方成功后应登出并跳登录页，用新密码重新登录换发新 token。
export function useChangePassword() {
  return useMutation({
    mutationFn: ({
      oldPassword,
      newPassword,
    }: {
      oldPassword: string;
      newPassword: string;
    }) => authApi.changePassword(oldPassword, newPassword),
  });
}
