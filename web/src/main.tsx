import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import "./index.css";
import App from "./App.tsx";

// QueryClient：全局数据获取/缓存。
// staleTime: 0 —— 每次组件 mount/focus 都 refetch，保证数据新鲜（告警系统需实时）。
// mutation 的 invalidateQueries 始终强制 refetch active query（与 staleTime 无关）。
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 0,
      retry: 1,
      refetchOnWindowFocus: true,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
      <Toaster richColors position="top-right" />
    </QueryClientProvider>
  </StrictMode>,
);

// PWA service worker 注册（P4·B3）：仅生产构建注册，使 Vigil 可安装 + 大屏离线壳可用。
// 开发态（import.meta.env.DEV）不注册——SW 的静态资源缓存会与 Vite HMR 冲突（返回旧模块）。
if ("serviceWorker" in navigator && import.meta.env.PROD) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {
      // 注册失败静默降级（PWA 是增强，不影响主功能）。
    });
  });
}
