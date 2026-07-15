import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { LogOut, Shield, Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { logout } from "@/lib/auth";
import { SUPPORTED_LANGS, setLanguage, type Lang } from "@/lib/i18n";
import { ThemeToggle } from "@/components/theme/theme-toggle";
import { NightDarkModePrompt } from "@/components/theme/night-prompt";

/**
 * AppShell —— 应用主布局：左侧导航 + 右侧内容区（Outlet）。
 * 导航项对应 Vigil 主要功能区。标签走 i18n（nav.*），随语言切换。
 */
const navItems = [
  { to: "/", labelKey: "nav.dashboard", end: true },
  { to: "/incidents", labelKey: "nav.incidents" },
  { to: "/oncall", labelKey: "nav.oncall" },
  { to: "/services", labelKey: "nav.services" },
  { to: "/maintenance", labelKey: "nav.maintenance" },
  { to: "/integrations", labelKey: "nav.integrations" },
  { to: "/webhook-subscriptions", labelKey: "nav.webhookSubscriptions" },
  { to: "/ticket-integrations", labelKey: "nav.ticketIntegrations" },
  { to: "/credentials", labelKey: "nav.credentials" },
  { to: "/escalation-policies", labelKey: "nav.escalationPolicies" },
  { to: "/users-teams", labelKey: "nav.usersTeams" },
  { to: "/runbooks", labelKey: "nav.runbooks" },
  { to: "/postmortems", labelKey: "nav.postmortems" },
  { to: "/settings", labelKey: "nav.settings" },
];

export function AppShell() {
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  const onLogout = () => {
    logout();
    navigate("/login", { replace: true });
  };

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
              {t(item.labelKey)}
            </NavLink>
          ))}
        </nav>
        {/* 主题切换（仅核心响应页显示，ADR-0034）+ 语言切换 + 登出 */}
        <div className="space-y-1 border-t p-2">
          <ThemeToggle />
          {/* 语言切换：中文 / English，changeLanguage + 写 localStorage 持久化 */}
          <div className="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-muted-foreground">
            <Languages className="h-4 w-4 shrink-0" />
            <select
              aria-label={t("nav.language")}
              value={i18n.language.startsWith("en") ? "en" : "zh"}
              onChange={(e) => setLanguage(e.target.value as Lang)}
              className="flex-1 cursor-pointer rounded-md border bg-transparent px-1.5 py-1 text-sm outline-none focus:ring-1 focus:ring-ring"
            >
              {SUPPORTED_LANGS.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </select>
          </div>
          {/* 登出（闭环登录态）：清 token 跳登录页 */}
          <button
            type="button"
            onClick={onLogout}
            className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <LogOut className="h-4 w-4" />
            {t("nav.logout")}
          </button>
        </div>
      </aside>

      {/* 内容区 */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>

      {/* 夜间(22:00–07:00)首访核心响应页的暗色强引导（ADR-0034），自带路由/时段/记忆判定 */}
      <NightDarkModePrompt />
    </div>
  );
}
