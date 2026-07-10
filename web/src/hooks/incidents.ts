/**
 * 数据 hooks —— 封装 react-query 的查询与变更。
 *
 * query key 统一在此声明，便于跨 hook invalidate（写操作成功后刷新列表/详情）。
 */
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, type ListIncidentsParams } from "@/lib/api";
import type { DashboardMetrics, Incident } from "@/lib/types";
import { toast } from "sonner";

// —— query keys ——
export const qk = {
  incidents: (params: ListIncidentsParams) => ["incidents", params] as const,
  incident: (id: number) => ["incident", id] as const,
  timeline: (id: number) => ["timeline", id] as const,
  dashboard: (days: number) => ["dashboard", days] as const,
};

// —— 查询 ——

/** useIncidents 事件列表（带筛选）。 */
export function useIncidents(params: ListIncidentsParams) {
  return useQuery({
    queryKey: qk.incidents(params),
    queryFn: () => api.listIncidents(params),
  });
}

/** useIncident 事件详情（含 responders/events）。 */
export function useIncident(id: number) {
  return useQuery({
    queryKey: qk.incident(id),
    queryFn: () => api.getIncident(id),
    enabled: !!id,
  });
}

/** useTimeline 事件时间线。 */
export function useTimeline(id: number) {
  return useQuery({
    queryKey: qk.timeline(id),
    queryFn: () => api.listTimeline(id),
    enabled: !!id,
  });
}

/** useDashboard 仪表盘汇总。 */
export function useDashboard(days = 7) {
  return useQuery<DashboardMetrics>({
    queryKey: qk.dashboard(days),
    queryFn: () => api.getDashboard(days),
  });
}

// —— 变更（写操作）——

type ActionKind = "ack" | "resolve" | "escalate" | "reopen";

const actionMeta: Record<ActionKind, { fn: (id: number) => Promise<Incident>; verb: string }> = {
  ack: { fn: api.ackIncident, verb: "确认" },
  resolve: { fn: api.resolveIncident, verb: "解决" },
  escalate: { fn: api.escalateIncident, verb: "升级" },
  reopen: { fn: api.reopenIncident, verb: "重新打开" },
};

/**
 * useIncidentAction 事件写操作（ack/resolve/escalate）。
 * 成功后：toast + invalidate 该事件详情/时间线 + 列表，使各页即时反映新状态。
 */
export function useIncidentAction(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (kind: ActionKind) => actionMeta[kind].fn(id),
    onSuccess: (data, kind) => {
      toast.success(`已${actionMeta[kind].verb}事件 ${data.number}`);
      // 刷新详情（直接写入返回值，更快）、时间线、列表
      qc.setQueryData(qk.incident(id), data);
      qc.invalidateQueries({ queryKey: qk.timeline(id) });
      qc.invalidateQueries({ queryKey: ["incidents"] });
    },
    onError: () => {
      // 错误提示已由 http 拦截器统一处理（toast）
    },
  });
}

/**
 * useAddTimelineNote 手动追加时间线备注（处置过程中的人工记录：联系了谁、试了什么）。
 * 成功后刷新时间线；toast 文案由调用方（页面）以 i18n 提示。
 */
export function useAddTimelineNote(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (content: string) => api.addTimelineNote(id, content),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.timeline(id) });
    },
    onError: () => {
      // 错误提示由 http 拦截器统一处理（toast）
    },
  });
}

/**
 * useMergeIncident 把源事件合并进本单（能力域 3 去重合并，不可逆）。
 * 成功后：源单被关闭、本单吸并 events/responders；刷新详情/时间线/列表。
 */
export function useMergeIncident(id: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sourceIncidentIds: number[]) => api.mergeIncident(id, sourceIncidentIds),
    onSuccess: (data) => {
      toast.success(`已合并事件到 ${data.number}`);
      qc.setQueryData(qk.incident(id), data);
      qc.invalidateQueries({ queryKey: qk.timeline(id) });
      qc.invalidateQueries({ queryKey: ["incidents"] });
    },
    onError: () => {
      // 错误提示由 http 拦截器统一处理（toast）
    },
  });
}
