import axios, { AxiosError, type AxiosInstance } from "axios";
import { toast } from "sonner";
import i18n from "@/lib/i18n";

/**
 * http —— 全局 axios 实例。
 * baseURL 走相对路径 /api/v1，开发态由 vite proxy 转发到后端 Echo，
 * 生产态由后端 embed 静态资源同源访问。
 */
export const http: AxiosInstance = axios.create({
  baseURL: "/api/v1",
  timeout: 30_000,
  headers: {
    "Content-Type": "application/json",
  },
});

// 请求拦截：附加鉴权与身份。
// 鉴权优先级与后端中间件一致：JWT Bearer（登录态）优先，X-Vigil-User-ID 回退（兼容降级）。
// 浏览器侧从 localStorage 取 token/userId 注入，避免每个调用方手写。
http.interceptors.request.use((config) => {
  // JWT 登录态（能力域 13）：登录后存 vigil_token
  const token = localStorage.getItem("vigil_token");
  if (token) {
    config.headers["Authorization"] = `Bearer ${token}`;
  }
  const apiKey = localStorage.getItem("vigil_api_key");
  if (apiKey) {
    config.headers["X-Vigil-Key"] = apiKey;
  }
  const userId = localStorage.getItem("vigil_user_id");
  if (userId) {
    config.headers["X-Vigil-User-ID"] = userId;
  }
  return config;
});

/** 非 JSON 2xx 响应的专用错误码，extractError 据此给出可诊断的提示（区别于普通请求失败）。 */
export const ERR_NON_JSON = "ERR_NON_JSON";

// 响应拦截：JSON 守卫。须注册在下方错误提示拦截器之前，reject 才会流经它统一提示。
// 后端 2xx 一律返回 JSON（或空 body）；拿到非空字符串，说明 /api 命中了错配的代理
// 或被无关服务占用的端口（返回 HTML 之类）——不在此拦截的话，调用方会把字符串当
// 业务对象往下用（如 inc.priority.toUpperCase()），页面直接白屏崩溃。
http.interceptors.response.use((response) => {
  const rt = response.config.responseType;
  const expectsJSON = !rt || rt === "json"; // blob/text 等显式声明的响应不设防（如审计导出）
  if (
    expectsJSON &&
    typeof response.data === "string" &&
    response.data.trim() !== ""
  ) {
    return Promise.reject(
      new AxiosError(
        "expected JSON but got non-JSON body",
        ERR_NON_JSON,
        response.config,
        response.request,
        response,
      ),
    );
  }
  return response;
});

// extractError 从后端响应里提取可读错误信息。
// 后端约定：{ "error": "..." }；无 body 时回退到 axios message。
export function extractError(error: unknown): string {
  if (axios.isAxiosError(error)) {
    if (error.code === ERR_NON_JSON) return i18n.t("errors.invalidResponse");
    const data = error.response?.data as { error?: string } | undefined;
    if (data?.error) return data.error;
    if (error.response) {
      return i18n.t("errors.requestFailed", { status: error.response.status });
    }
    if (error.request) return i18n.t("errors.networkNoResponse");
    return error.message;
  }
  return i18n.t("errors.unknown");
}

// 响应拦截：统一错误提示（接 sonner toast）。
// 仍 reject 以便调用方（如 react-query）感知错误做 loading/重试处理。
// 401 特殊处理：登录态失效时清 token 跳登录页（避免递归——登录页自身的 401 不触发重定向）。
http.interceptors.response.use(
  (response) => response,
  (error) => {
    // 401 登录态失效：清凭据跳登录（登录页自身 401 除外，防递归）
    if (axios.isAxiosError(error) && error.response?.status === 401) {
      localStorage.removeItem("vigil_token");
      localStorage.removeItem("vigil_refresh_token");
      localStorage.removeItem("vigil_user_id");
      if (!window.location.pathname.startsWith("/login")) {
        window.location.href = "/login";
      }
    }
    const message = extractError(error);
    console.error("[vigil] request error:", message, error);
    toast.error(message);
    return Promise.reject(error);
  },
);
