/**
 * AI 诊断 hooks（能力域 11）。
 * 对接后端：POST /incidents/:id/diagnose、GET /incidents/:id/similar、POST /ai-insights/:id/resolve。
 *
 * 设计基线第 4 条：AI 产出带 evidence + human-in-the-loop（resolveInsight）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

export const aiQk = {
  similar: (id: number) => ["ai", "similar-incidents", id] as const,
};

/** useDiagnoseIncident 触发根因诊断（mutation）。结果不缓存，由组件本地 state 承载。 */
export function useDiagnoseIncident() {
  return useMutation({
    mutationFn: (incidentId: number) => api.diagnoseIncident(incidentId),
    onSuccess: (data) => {
      if ("status" in data && data.status === "disabled") {
        toast.info(data.message || "AI 诊断未启用");
      } else {
        toast.success("AI 诊断完成");
      }
    },
  });
}

/** useSimilarIncidents 查询相似历史事件（按 incidentId 缓存，AI 面板展开时加载）。 */
export function useSimilarIncidents(incidentId: number, enabled: boolean) {
  return useQuery({
    queryKey: aiQk.similar(incidentId),
    queryFn: () => api.findSimilarIncidents(incidentId, 5),
    enabled: enabled && !!incidentId,
  });
}

/** useResolveInsight 人确认/拒绝 AI 建议（human-in-the-loop）。 */
export function useResolveInsight(incidentId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { insightId: number; accepted: boolean }) =>
      api.resolveAiInsight(args.insightId, args.accepted),
    onSuccess: (_data, vars) => {
      toast.success(vars.accepted ? "已采纳 AI 诊断" : "已拒绝 AI 诊断");
      // 诊断结果在组件本地 state，无需 invalidate；刷新相似事件以便后续联动。
      qc.invalidateQueries({ queryKey: aiQk.similar(incidentId) });
    },
  });
}
