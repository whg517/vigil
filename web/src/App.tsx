import { Routes, Route, Navigate } from "react-router-dom";
import type { ReactNode } from "react";
import { AppShell } from "@/components/layout/app-shell";
import { Dashboard } from "@/pages/dashboard";
import { Incidents } from "@/pages/incidents";
import { IncidentDetail } from "@/pages/incident-detail";
import { Oncall } from "@/pages/oncall";
import { Services } from "@/pages/services";
import { Integrations } from "@/pages/integrations";
import { TicketIntegrations } from "@/pages/ticket-integrations";
import { Credentials } from "@/pages/credentials";
import { EscalationPolicies } from "@/pages/escalation-policies";
import { UsersTeams } from "@/pages/users-teams";
import { Runbooks } from "@/pages/runbooks";
import { Postmortems } from "@/pages/postmortems";
import { Settings } from "@/pages/settings";
import { Login } from "@/pages/login";
import { ChangePassword } from "@/pages/change-password";
import { isAuthenticated, mustChangePassword } from "@/lib/auth";

/**
 * RequireAuth 路由守卫：无 token 时重定向到登录页。
 * 仅本地 token 判断（后端 JWT 校验在中间件）；token 失效时 401 拦截器会清 token 并重定向。
 *
 * T0.4 首登强制改密闭环：已登录但 must_change_password=true 时，强制重定向到改密页，
 * 其余业务页一律不可访问（与后端 forcePasswordGuard 前后端一致——后端也会 403 拦截）。
 */
function RequireAuth({ children }: { children: ReactNode }) {
  if (!isAuthenticated()) {
    return <Navigate to="/login" replace />;
  }
  if (mustChangePassword()) {
    return <Navigate to="/change-password" replace />;
  }
  return <>{children}</>;
}

/**
 * App —— 应用根：路由表。
 * /login、/change-password 独立于 AppShell（无侧边栏）；业务页 RequireAuth 保护。
 */
function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/change-password" element={<ChangePassword />} />
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
        <Route path="integrations" element={<Integrations />} />
        <Route path="ticket-integrations" element={<TicketIntegrations />} />
        <Route path="credentials" element={<Credentials />} />
        <Route path="escalation-policies" element={<EscalationPolicies />} />
        <Route path="users-teams" element={<UsersTeams />} />
        <Route path="runbooks" element={<Runbooks />} />
        <Route path="postmortems" element={<Postmortems />} />
        <Route path="settings" element={<Settings />} />
      </Route>
    </Routes>
  );
}

export default App;
