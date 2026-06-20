import axios, { type AxiosInstance } from "axios";
import { toast } from "sonner";

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
// 身份：后端 RequireUser 中间件从 X-Vigil-User-ID 解析（渐进式鉴权阶段），
// 浏览器侧从 localStorage 取当前用户 ID 注入，避免每个调用方手写。
http.interceptors.request.use((config) => {
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
      return `请求失败（${error.response.status}）`;
    }
    if (error.request) return "网络无响应，请检查后端是否启动";
    return error.message;
  }
  return "未知错误";
}

// 响应拦截：统一错误提示（接 sonner toast）。
// 仍 reject 以便调用方（如 react-query）感知错误做 loading/重试处理。
http.interceptors.response.use(
  (response) => response,
  (error) => {
    const message = extractError(error);
    console.error("[vigil] request error:", message, error);
    toast.error(message);
    return Promise.reject(error);
  },
);
