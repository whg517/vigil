/**
 * 登录态 API client（能力域 13 §登录态）。
 *
 * 与 lib/api.ts 分离：token 流程独立封装，避免 api.ts 在未登录时被 import 触发副作用。
 * 凭据存储：localStorage（vigil_token / vigil_refresh_token / vigil_user_id）。
 */
import { http } from "@/lib/http";

/** localStorage 键：首登强制改密标志（T0.4）。"1" 表示需改密。 */
const MUST_CHANGE_KEY = "vigil_must_change_password";

/** 当前登录用户（me 接口返回，裁剪敏感字段） */
export interface CurrentUser {
  id: number;
  username: string;
  name?: string;
  email: string;
  status: string;
  /** 首登强制改密标志（T0.4）：为 true 时前端强制跳改密页，未改密不得访问业务页。 */
  must_change_password?: boolean;
}

interface LoginResponse {
  access_token: string;
  refresh_token: string;
  token_type: string;
  user: CurrentUser;
}

// syncMustChange 把 must_change_password 落到 localStorage（供同步的路由守卫读取）。
// 之所以持久化：RequireAuth 需同步判断是否放行，不能每次导航都等 me 网络往返。
function syncMustChange(must: boolean | undefined) {
  if (must) {
    localStorage.setItem(MUST_CHANGE_KEY, "1");
  } else {
    localStorage.removeItem(MUST_CHANGE_KEY);
  }
}

export const authApi = {
  /** 登录：username+password 换 access+refresh token。成功后存 localStorage。 */
  async login(username: string, password: string): Promise<LoginResponse> {
    const resp = await http.post<LoginResponse>("/auth/login", { username, password });
    const data = resp.data;
    localStorage.setItem("vigil_token", data.access_token);
    localStorage.setItem("vigil_refresh_token", data.refresh_token);
    localStorage.setItem("vigil_user_id", String(data.user.id));
    // 记录强制改密标志（T0.4）：登录页据此决定是否跳改密页。
    syncMustChange(data.user.must_change_password);
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
  /** 当前用户信息（需已登录）。同时同步强制改密标志，保证页面刷新后守卫仍准确。 */
  async me(): Promise<CurrentUser> {
    const resp = await http.get<CurrentUser>("/auth/me");
    syncMustChange(resp.data.must_change_password);
    return resp.data;
  },
  /** 修改密码：校验旧密码 + 设新密码。成功后后端清除强制改密标志，前端同步清除。 */
  async changePassword(oldPassword: string, newPassword: string): Promise<void> {
    await http.post("/auth/change-password", {
      old_password: oldPassword,
      new_password: newPassword,
    });
    syncMustChange(false);
  },
};

/** 登出：清除本地凭据（不调后端，JWT 无状态，客户端清除即生效）。 */
export function logout() {
  localStorage.removeItem("vigil_token");
  localStorage.removeItem("vigil_refresh_token");
  localStorage.removeItem("vigil_user_id");
  localStorage.removeItem(MUST_CHANGE_KEY);
}

/** 是否已登录（本地有 token）。 */
export function isAuthenticated(): boolean {
  return !!localStorage.getItem("vigil_token");
}

/** 是否处于强制改密态（T0.4）：为 true 时业务页不可访问，须先改密。 */
export function mustChangePassword(): boolean {
  return localStorage.getItem(MUST_CHANGE_KEY) === "1";
}
