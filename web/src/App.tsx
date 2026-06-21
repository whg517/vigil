import { Routes, Route, Navigate } from "react-router-dom";
import type { ReactNode } from "react";
import { AppShell } from "@/components/layout/app-shell";
import { Dashboard } from "@/pages/dashboard";
import { Incidents } from "@/pages/incidents";
import { IncidentDetail } from "@/pages/incident-detail";
import { Oncall } from "@/pages/oncall";
import { Services } from "@/pages/services";
import { Runbooks } from "@/pages/runbooks";
import { Postmortems } from "@/pages/postmortems";
import { Settings } from "@/pages/settings";
import { Login } from "@/pages/login";
import { isAuthenticated } from "@/lib/auth";

/**
 * RequireAuth 路由守卫：无 token 时重定向到登录页。
 * 仅本地 token 判断（后端 JWT 校验在中间件）；token 失效时 401 拦截器会清 token 并重定向。
 */
function RequireAuth({ children }: { children: ReactNode }) {
  if (!isAuthenticated()) {
    return <Navigate to="/login" replace />;
  }
  return <>{children}</>;
}

/**
 * App —— 应用根：路由表。
 * /login 独立于 AppShell（无侧边栏）；业务页 RequireAuth 保护。
 */
function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="incidents" element={<Incidents />} />
        <Route path="incidents/:id" element={<IncidentDetail />} />
        <Route path="oncall" element={<Oncall />} />
        <Route path="services" element={<Services />} />
        <Route path="runbooks" element={<Runbooks />} />
        <Route path="postmortems" element={<Postmortems />} />
        <Route path="settings" element={<Settings />} />
      </Route>
    </Routes>
  );
}

export default App;
