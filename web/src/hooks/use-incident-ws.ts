/**
 * useIncidentWS 订阅 incident 的 WebSocket 变更推送（能力域 8 §状态双向同步）。
 *
 * 收到推送后 invalidate 对应的 React Query 缓存：
 *   - incident_changed → 刷新 incident 详情 + 列表
 *   - timeline_added → 刷新该 incident 的时间线
 *
 * 用法：在 incident-detail 页 useEffect 调用，组件卸载自动退订。
 */
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { subscribeIncident, type WSMessage } from "@/lib/ws";

export function useIncidentWS(incidentId: number) {
  const qc = useQueryClient();

  useEffect(() => {
    const cleanup = subscribeIncident(incidentId, (msg: WSMessage) => {
      switch (msg.type) {
        case "incident_changed":
          // 刷新 incident 详情（本地已有数据时直接 setQueryData 加速）
          qc.invalidateQueries({ queryKey: ["incident", incidentId] });
          // 列表也刷新（状态变了，列表里那条要更新）
          qc.invalidateQueries({ queryKey: ["incidents"] });
          // 时间线随之刷新（状态变更通常伴随时间线条目）
          qc.invalidateQueries({ queryKey: ["timeline", incidentId] });
          break;
        case "timeline_added":
          qc.invalidateQueries({ queryKey: ["timeline", incidentId] });
          break;
      }
    });
    return cleanup;
  }, [incidentId, qc]);
}
