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
