/**
 * theme.ts —— 暗色模式的状态与判定逻辑（ADR-0034）。
 *
 * 设计要点（为什么这么做）：
 * - 亮色默认、暗色仅作用于「核心响应页」（事件列表/详情）——ADR-0034 明确否决全站暗色：
 *   多数后台操作发生在白天，全暗违背使用习惯；深夜处置才需要暗色。
 * - 「非全站暗色」的落地方式：主题偏好为暗色 **且** 当前路由是核心响应页时，
 *   才把 `.dark` 挂到 <html>；离开核心页即摘除。挂根元素而非只包页面容器，
 *   是为了让 Dialog 遮罩、toast、原生滚动条等游离层与页面一起变暗，
 *   避免深夜弹出一块刺眼的亮色浮层（半夜能用原则）。
 * - 主题偏好持久化到 localStorage（与语言选择 vigil_lang 同一模式）。
 */
import { createContext, useContext } from "react";

export const THEME_KEY = "vigil_theme";
/** 夜间引导「不再提醒」持久化键（localStorage，跨会话永久静默）。 */
export const NIGHT_PROMPT_DISMISSED_KEY = "vigil_night_prompt_dismissed";
/** 夜间引导「本会话已提示」键（sessionStorage）——“首次访问”只弹一次，页面间导航不重复打扰。 */
export const NIGHT_PROMPT_SESSION_KEY = "vigil_night_prompt_shown";

export type Theme = "light" | "dark";

/**
 * 核心响应页路由前缀（ADR-0034「事件详情等核心响应页」）。
 * 当前为事件列表 + 事件详情；后续要扩大暗色覆盖面（如仪表盘）在此追加即可，
 * 边界收在一处，避免各组件各自判断导致口径漂移。
 */
const CORE_RESPONSE_PREFIXES = ["/incidents"];

/** 当前路径是否属于核心响应页。 */
export function isCoreResponsePath(pathname: string): boolean {
  return CORE_RESPONSE_PREFIXES.some(
    (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
  );
}

/** 读取持久化主题；未设置或值非法时回落亮色（ADR-0034 亮色默认）。 */
export function getStoredTheme(): Theme {
  return localStorage.getItem(THEME_KEY) === "dark" ? "dark" : "light";
}

export function setStoredTheme(theme: Theme): void {
  localStorage.setItem(THEME_KEY, theme);
}

/** 是否处于夜间时段 22:00–07:00（ADR-0034 强引导窗口）；按浏览器本地时区——引导服务的是坐在屏幕前的人。 */
export function isNightHours(now: Date = new Date()): boolean {
  const hour = now.getHours();
  return hour >= 22 || hour < 7;
}

export interface ThemeContextValue {
  /** 用户主题偏好（持久化值）。 */
  theme: Theme;
  /** 当前是否实际渲染为暗色 = 偏好为暗色 && 身处核心响应页。 */
  resolvedDark: boolean;
  setTheme: (theme: Theme) => void;
}

/** 默认值仅为兜底（未包 Provider 时保持亮色不报错），正常渲染路径必经 ThemeProvider。 */
export const ThemeContext = createContext<ThemeContextValue>({
  theme: "light",
  resolvedDark: false,
  setTheme: () => {},
});

export function useTheme(): ThemeContextValue {
  return useContext(ThemeContext);
}
