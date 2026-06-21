import { Routes, Route } from "react-router-dom";
import { AppShell } from "@/components/layout/app-shell";
import { Dashboard } from "@/pages/dashboard";
import { Incidents } from "@/pages/incidents";
import { IncidentDetail } from "@/pages/incident-detail";
import { Oncall } from "@/pages/oncall";
import { Services } from "@/pages/services";
import { Runbooks } from "@/pages/runbooks";
import { Postmortems } from "@/pages/postmortems";
import { Settings } from "@/pages/settings";

/**
 * App —— 应用根：路由表。
 * 已落地全部业务页面：仪表盘、事件、值班、服务、Runbook、复盘、设置。
 */
function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
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
