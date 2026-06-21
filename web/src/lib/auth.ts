/**
 * 登录态 API client（能力域 13 §登录态）。
 *
 * 与 lib/api.ts 分离：token 流程独立封装，避免 api.ts 在未登录时被 import 触发副作用。
 * 凭据存储：localStorage（vigil_token / vigil_refresh_token / vigil_user_id）。
 */
import { http } from "@/lib/http";

/** 当前登录用户（me 接口返回，裁剪敏感字段） */
export interface CurrentUser {
  id: number;
  username: string;
  name?: string;
  email: string;
  status: string;
}

interface LoginResponse {
  access_token: string;
  refresh_token: string;
  token_type: string;
  user: CurrentUser;
}

export const authApi = {
  /** 登录：username+password 换 access+refresh token。成功后存 localStorage。 */
  async login(username: string, password: string): Promise<LoginResponse> {
    const resp = await http.post<LoginResponse>("/auth/login", { username, password });
    const data = resp.data;
    localStorage.setItem("vigil_token", data.access_token);
    localStorage.setItem("vigil_refresh_token", data.refresh_token);
    localStorage.setItem("vigil_user_id", String(data.user.id));
    return data;
  },
  /** 刷新：用 refresh token 换新 access token。 */
  async refresh(refreshToken: string): Promise<string> {
    const resp = await http.post<{ access_token: string }>("/auth/refresh", {
      refresh_token: refreshToken,
    });
    const accessToken = resp.data.access_token;
    localStorage.setItem("vigil_token", accessToken);
    return accessToken;
  },
  /** 当前用户信息（需已登录）。 */
  async me(): Promise<CurrentUser> {
    const resp = await http.get<CurrentUser>("/auth/me");
    return resp.data;
  },
};

/** 登出：清除本地凭据（不调后端，JWT 无状态，客户端清除即生效）。 */
export function logout() {
  localStorage.removeItem("vigil_token");
  localStorage.removeItem("vigil_refresh_token");
  localStorage.removeItem("vigil_user_id");
}

/** 是否已登录（本地有 token）。 */
export function isAuthenticated(): boolean {
  return !!localStorage.getItem("vigil_token");
}
