import { Moon, Sun } from "lucide-react";
import { useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { isCoreResponsePath, useTheme } from "@/lib/theme";

/**
 * ThemeToggle —— 暗色/亮色切换按钮（放在侧边栏底部，与语言切换同区）。
 * 只在核心响应页路由下渲染：暗色只作用于这些页（ADR-0034 非全站暗色），
 * 在其他页面展示一个「按了没反应」的开关只会造成困惑。
 */
export function ThemeToggle() {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const { theme, setTheme } = useTheme();

  if (!isCoreResponsePath(pathname)) return null;

  const dark = theme === "dark";
  return (
    <button
      type="button"
      onClick={() => setTheme(dark ? "light" : "dark")}
      title={t("theme.scopeHint")}
      className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
    >
      {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
      {dark ? t("theme.toggleToLight") : t("theme.toggleToDark")}
    </button>
  );
}
