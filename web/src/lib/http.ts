import axios, { type AxiosInstance } from "axios";
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

// extractError 从后端响应里提取可读错误信息。
// 后端约定：{ "error": "..." }；无 body 时回退到 axios message。
export function extractError(error: unknown): string {
  if (axios.isAxiosError(error)) {
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
