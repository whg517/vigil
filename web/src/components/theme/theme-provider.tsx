import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { Toaster } from "sonner";
import {
  ThemeContext,
  type Theme,
  type ThemeContextValue,
  getStoredTheme,
  setStoredTheme,
  isCoreResponsePath,
  useTheme,
} from "@/lib/theme";

/**
 * ThemeProvider —— 主题状态源 + 「核心响应页才挂 .dark」的路由联动（ADR-0034）。
 * 必须置于 BrowserRouter 内：暗色是否生效取决于当前路由是否核心响应页（非全站暗色）。
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const [theme, setThemeState] = useState<Theme>(getStoredTheme);

  // 非全站暗色的关键落点：偏好暗色 && 核心响应页 才渲染暗色，路由离开即回亮色。
  const resolvedDark = theme === "dark" && isCoreResponsePath(pathname);

  useEffect(() => {
    // 挂 <html>（而非页面容器）：让 Dialog 遮罩、toast、原生控件/滚动条一起变暗，
    // 避免夜间出现刺眼亮斑；「非全站」边界由上面的路由判定保证。
    document.documentElement.classList.toggle("dark", resolvedDark);
  }, [resolvedDark]);

  const value = useMemo<ThemeContextValue>(
    () => ({
      theme,
      resolvedDark,
      setTheme: (next: Theme) => {
        setStoredTheme(next);
        setThemeState(next);
      },
    }),
    [theme, resolvedDark],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

/**
 * ThemedToaster —— 跟随主题的全局 toast。
 * sonner 不识别 .dark class，需显式传 theme；否则暗色详情页上弹出亮色 toast，正是夜间要避免的亮斑。
 */
export function ThemedToaster() {
  const { resolvedDark } = useTheme();
  return (
    <Toaster
      richColors
      position="top-right"
      theme={resolvedDark ? "dark" : "light"}
    />
  );
}
