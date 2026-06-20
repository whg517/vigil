import { NavLink, Outlet } from "react-router-dom";
import { Shield } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * AppShell —— 应用主布局：左侧导航 + 右侧内容区（Outlet）。
 * 导航项对应 Vigil 主要功能区，业务页面待实现。
 */
const navItems = [
  { to: "/", label: "仪表盘", end: true },
  { to: "/incidents", label: "事件" },
  { to: "/oncall", label: "值班排班" },
  { to: "/services", label: "服务" },
  { to: "/runbooks", label: "Runbook" },
  { to: "/postmortems", label: "复盘" },
  { to: "/settings", label: "设置" },
];

export function AppShell() {
  return (
    <div className="flex h-screen w-full overflow-hidden">
      {/* 侧边栏 */}
      <aside className="flex w-56 shrink-0 flex-col border-r bg-card">
        <div className="flex h-14 items-center gap-2 border-b px-4">
          <Shield className="h-5 w-5 text-primary" />
          <span className="text-base font-semibold">Vigil</span>
        </div>
        <nav className="flex-1 space-y-1 p-2">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                cn(
                  "block rounded-md px-3 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                )
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
      </aside>

      {/* 内容区 */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}
