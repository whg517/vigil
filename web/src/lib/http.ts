import axios, { type AxiosInstance } from "axios";

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

// 请求拦截：附加鉴权（后续接登录态/API Key）
http.interceptors.request.use((config) => {
  const apiKey = localStorage.getItem("vigil_api_key");
  if (apiKey) {
    config.headers["X-Vigil-Key"] = apiKey;
  }
  return config;
});

// 响应拦截：统一错误处理（后续接全局错误提示）
http.interceptors.response.use(
  (response) => response,
  (error) => {
    // 占位：后续接入 toast/全局错误边界
    console.error("[vigil] request error:", error?.message);
    return Promise.reject(error);
  },
);
