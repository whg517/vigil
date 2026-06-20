import { Routes, Route } from "react-router-dom";
import { AppShell } from "@/components/layout/app-shell";
import { Dashboard } from "@/pages/dashboard";
import { Incidents } from "@/pages/incidents";
import { IncidentDetail } from "@/pages/incident-detail";

/**
 * App —— 应用根：路由表。
 * 已落地：仪表盘、事件列表、事件详情。
 * 其余页面（值班/服务/Runbook/复盘/设置）为占位，待按能力域实现。
 */
function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Dashboard />} />
        <Route path="incidents" element={<Incidents />} />
        <Route path="incidents/:id" element={<IncidentDetail />} />
        <Route path="oncall" element={<Placeholder title="值班排班" />} />
        <Route path="services" element={<Placeholder title="服务" />} />
        <Route path="runbooks" element={<Placeholder title="Runbook" />} />
        <Route path="postmortems" element={<Placeholder title="复盘" />} />
        <Route path="settings" element={<Placeholder title="设置" />} />
      </Route>
    </Routes>
  );
}

/** Placeholder —— 业务页面占位（待实现）。 */
function Placeholder({ title }: { title: string }) {
  return (
    <div className="p-6">
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      <p className="mt-2 text-sm text-muted-foreground">
        该页面待实现（参见 docs/capabilities/ 对应能力域设计）。
      </p>
    </div>
  );
}

export default App;
