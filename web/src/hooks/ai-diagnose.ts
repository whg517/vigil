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
  insights: (id: number) => ["ai", "insights", id] as const,
};

/** useIncidentInsights 加载某 incident 的历史 AI 洞察（T3.1 可读持久化）。
 * 诊断产出落库后可持久查看，accept/reject/applied 状态持久呈现（刷新不丢）。 */
export function useIncidentInsights(incidentId: number) {
  return useQuery({
    queryKey: aiQk.insights(incidentId),
    queryFn: () => api.listIncidentInsights(incidentId),
    enabled: !!incidentId,
  });
}

/** useDiagnoseIncident 触发根因诊断（mutation）。结果不缓存，由组件本地 state 承载；
 * 成功后使洞察列表失效，让新产出的洞察进入历史列表。 */
export function useDiagnoseIncident(incidentId?: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (incidentId: number) => api.diagnoseIncident(incidentId),
    onSuccess: (data) => {
      if ("status" in data && data.status === "disabled") {
        toast.info(data.message || "AI 诊断未启用");
      } else {
        toast.success("AI 诊断完成");
        if (incidentId) {
          qc.invalidateQueries({ queryKey: aiQk.insights(incidentId) });
        }
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

/** useResolveInsight 人确认/拒绝 AI 建议（human-in-the-loop）。
 * 后端返回终态 insight_status（accepted/applied/rejected）；成功后使洞察列表失效，
 * 让持久化的状态（含 applied 生命周期）反映到历史列表。 */
export function useResolveInsight(incidentId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { insightId: number; accepted: boolean }) =>
      api.resolveAiInsight(args.insightId, args.accepted),
    onSuccess: (data, vars) => {
      // applied：不仅采纳，还实际应用了副作用（如严重度已改），给更明确的提示。
      if (vars.accepted && data.insight_status === "applied") {
        toast.success("已采纳并应用 AI 建议");
      } else {
        toast.success(vars.accepted ? "已采纳 AI 诊断" : "已拒绝 AI 诊断");
      }
      // 刷新洞察列表（状态持久化）与相似事件。
      qc.invalidateQueries({ queryKey: aiQk.insights(incidentId) });
      qc.invalidateQueries({ queryKey: aiQk.similar(incidentId) });
    },
  });
}
