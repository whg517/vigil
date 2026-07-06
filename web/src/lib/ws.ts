/**
 * WebSocket 客户端（能力域 8 §状态双向同步）。
 *
 * 用浏览器原生 WebSocket（无额外依赖），走 vite 已配的 /ws proxy。
 * 订阅 incident 变更，收到推送后由调用方（hook）刷新 React Query 缓存。
 *
 * 连接管理：自动重连（指数退避，上限 30s），组件卸载时关闭。
 */
export interface WSMessage {
  type: "incident_changed" | "timeline_added" | "dashboard_update";
  incident_id: number;
  action?: string;
  data?: unknown;
}

type MessageHandler = (msg: WSMessage) => void;

/**
 * subscribePath 通用 WS 订阅：连到 /api/v1 下某端点路径，收到消息回调，断线自动重连。
 * subscribeIncident / subscribeDashboard 都基于它，只是路径不同（避免重连逻辑重复两份）。
 *
 * @param path WS 端点相对路径（如 `/ws/incidents/1`、`/ws/dashboard`）
 * @param onMessage 收到消息的回调
 * @returns cleanup 函数（关闭连接，组件卸载时调用）
 */
function subscribePath(path: string, onMessage: MessageHandler): () => void {
  let ws: WebSocket | null = null;
  let closed = false; // 主动关闭标志（避免重连）
  let reconnectDelay = 1000;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  const connect = () => {
    if (closed) return;
    // 构造 ws URL：同源（生产同源 / 开发 vite proxy /api）。
    // WS 端点注册在 /api/v1 group（与 HTTP 业务路由同前缀），故需 /api/v1 前缀。
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    // 握手鉴权（T0.5）：后端在 Upgrade 前校验 ?token=<jwt>——无 token/无权即 401/403 拒握手。
    // 浏览器 WebSocket 握手无法带 Authorization 头，故令牌只能走 query。
    // token 从 http client 同源（localStorage vigil_token）读取，与 REST 请求同一凭据。
    //
    // 刷新场景：token 在每次 connect() 内即时读取（非闭包捕获一次），所以断线重连
    // （onclose → connect）总是带最新的 vigil_token。当前前端尚无自动刷新链路
    // （access token 过期时 http 拦截器直接清凭据跳登录，authApi.refresh 未接入自动流程），
    // 故 access token 过期后 WS 握手会被 401 拒、指数退避重试，直到用户重新登录写入新 token。
    // TODO（依赖自动 token 刷新落地）：接入 refresh 后，刷新写回 vigil_token 即被下次重连自动采用，
    // 本处无需改动；如需刷新后立即重连，可在刷新成功事件里主动关连接触发 onclose→connect。
    const token = localStorage.getItem("vigil_token") ?? "";
    const query = token ? `?token=${encodeURIComponent(token)}` : "";
    const url = `${proto}//${window.location.host}/api/v1${path}${query}`;
    ws = new WebSocket(url);

    ws.onopen = () => {
      // 握手成功即重置退避（下次断线从 1s 起步，避免长连偶断后仍以大延迟重连）。
      reconnectDelay = 1000;
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data) as WSMessage;
        onMessage(msg);
      } catch {
        // 忽略无法解析的消息
      }
    };

    ws.onclose = () => {
      if (closed) return;
      // 非主动关闭 → 自动重连（指数退避，上限 30s）
      reconnectDelay = Math.min(reconnectDelay * 1.5, 30000);
      reconnectTimer = setTimeout(connect, reconnectDelay);
    };

    ws.onerror = () => {
      // 错误后 onclose 会触发，重连逻辑在 onclose 处理
      ws?.close();
    };
  };

  connect();

  // cleanup：标记关闭 + 关连接 + 清定时器
  return () => {
    closed = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    if (ws) {
      ws.onclose = null; // 避免触发重连
      ws.close();
    }
  };
}

/**
 * subscribeIncident 订阅某 incident 的实时变更。
 * 返回 cleanup 函数（关闭连接，组件卸载时调用）。
 *
 * @param incidentId 订阅的 incident ID
 * @param onMessage 收到消息的回调
 * @returns cleanup 函数
 */
export function subscribeIncident(incidentId: number, onMessage: MessageHandler): () => void {
  return subscribePath(`/ws/incidents/${incidentId}`, onMessage);
}

/**
 * subscribeDashboard 订阅看板实时增量（值班大屏/仪表盘实时化，P4·B3）。
 * 任一 incident 生命周期事件发生时，后端向 /ws/dashboard 订阅者广播 dashboard_update；
 * 另有定时心跳（action=tick）兜底聚合类 KPI 的周期刷新。
 *
 * 需要 org 级 analytics.view（后端握手校验），无权则 403 拒握手 → 退避重试。
 *
 * @param onMessage 收到消息的回调
 * @returns cleanup 函数
 */
export function subscribeDashboard(onMessage: MessageHandler): () => void {
  return subscribePath(`/ws/dashboard`, onMessage);
}
