/**
 * format.ts —— 纯函数格式化工具（无 React 依赖）。
 * 从 format.tsx 拆出，避免组件文件混导函数（react-refresh 规则）。
 *
 * 时长单位（秒/分/时）随语言切换：用 i18n 单例的 t() 而非 useTranslation hook，
 * 因为这些是纯函数、在渲染回调里被调用，不是 React 组件（无法用 hook）。
 */
import i18n from "@/lib/i18n";

/**
 * formatTime 把 ISO 时间串格式化为本地可读形式。
 * 无值（解析失败）时返回 "—"。
 */
export function formatTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  // YYYY-MM-DD HH:mm
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** formatDuration 把秒数格式化为可读时长（单位随语言：秒/分/时 或 s/m/h）。 */
export function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 60) return i18n.t("format.durationSeconds", { n: Math.round(seconds) });
  const m = Math.round(seconds / 60);
  if (m < 60) return i18n.t("format.durationMinutes", { n: m });
  const h = Math.floor(m / 60);
  return i18n.t("format.durationHoursMinutes", { h, m: m % 60 });
}
