/**
 * useDashboardWS 订阅看板 WebSocket 实时增量（值班大屏/仪表盘实时化，P4·B3）。
 *
 * 收到 dashboard_update（任一 incident 生命周期事件 / 定时 tick）时 invalidate 仪表盘查询，
 * 使 KPI / 活跃事件列表 / 团队负载免轮询即刷新。
 *
 * 需要 org 级 analytics.view（后端 /ws/dashboard 握手校验）；无权时握手被拒、退避重试，
 * 页面仍靠 React Query 的常规拉取兜底（不因 WS 不可用而白屏）。
 *
 * 用法：在仪表盘 / 大屏页 useEffect 内调用，组件卸载自动退订。
 * @param connected 可选连接状态回调（用于大屏显示「实时/重连中」指示）。
 */
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { subscribeDashboard, type WSMessage } from "@/lib/ws";

export function useDashboardWS(onMessage?: (msg: WSMessage) => void) {
  const qc = useQueryClient();

  useEffect(() => {
    const cleanup = subscribeDashboard((msg: WSMessage) => {
      if (msg.type === "dashboard_update") {
        // 增量信号：让所有 dashboard 查询（不同 days 参数）重拉最新聚合。
        qc.invalidateQueries({ queryKey: ["dashboard"] });
        // 活跃事件列表随之刷新（大屏活跃看板复用 incidents 查询）。
        qc.invalidateQueries({ queryKey: ["incidents"] });
      }
      onMessage?.(msg);
    });
    return cleanup;
    // onMessage 由调用方保证稳定（或用 useCallback）；不入依赖避免频繁重订阅。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [qc]);
}
