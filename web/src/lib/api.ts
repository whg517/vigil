/**
 * api —— 类型化后端 client，封装 lib/http 的 axios 实例。
 *
 * 对应后端路由（见 internal 下各包的 handler.go Register 方法）：
 *   GET  /incidents                list（?status=&severity=&limit=&offset=）
 *   GET  /incidents/:id            detail（含 responders/events）
 *   POST /incidents/:id/ack
 *   POST /incidents/:id/resolve
 *   POST /incidents/:id/escalate
 *   GET  /incidents/:id/timeline
 *   GET  /analytics/dashboard      仪表盘汇总（?days=7）
 *
 * 注：actor 由 http 拦截器经 X-Vigil-User-ID 注入（后端从鉴权 context 取），
 *     故写操作调用方无需传 actor_id。
 */
import { http } from "@/lib/http";
import type {
  DashboardMetrics,
  Incident,
  ListResponse,
  TimelineItem,
} from "@/lib/types";

export interface ListIncidentsParams {
  status?: string;
  severity?: string;
  limit?: number;
  offset?: number;
}

export const api = {
  // —— Incident ——
  listIncidents(params: ListIncidentsParams = {}) {
    return http
      .get<ListResponse<Incident>>("/incidents", { params })
      .then((r) => r.data);
  },

  getIncident(id: number) {
    return http.get<Incident>(`/incidents/${id}`).then((r) => r.data);
  },

  ackIncident(id: number) {
    // body 不再传 actor（后端从鉴权 context 取）；空 body 即可
    return http
      .post<Incident>(`/incidents/${id}/ack`, {})
      .then((r) => r.data);
  },

  resolveIncident(id: number) {
    return http
      .post<Incident>(`/incidents/${id}/resolve`, {})
      .then((r) => r.data);
  },

  escalateIncident(id: number) {
    return http
      .post<Incident>(`/incidents/${id}/escalate`, {})
      .then((r) => r.data);
  },

  // —— Timeline ——
  listTimeline(incidentId: number) {
    return http
      .get<ListResponse<TimelineItem>>(`/incidents/${incidentId}/timeline`)
      .then((r) => r.data);
  },

  // —— Analytics ——
  getDashboard(days = 7) {
    return http
      .get<DashboardMetrics>("/analytics/dashboard", { params: { days } })
      .then((r) => r.data);
  },
};
