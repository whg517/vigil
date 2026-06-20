/**
 * format.ts —— 纯函数格式化工具（无 React 依赖）。
 * 从 format.tsx 拆出，避免组件文件混导函数（react-refresh 规则）。
 */

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

/** formatDuration 把秒数格式化为 "X分Y秒"/"X时Y分" 等可读时长。 */
export function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)}秒`;
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m}分`;
  const h = Math.floor(m / 60);
  return `${h}时${m % 60}分`;
}
