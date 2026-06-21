/**
 * WebSocket 客户端（能力域 8 §状态双向同步）。
 *
 * 用浏览器原生 WebSocket（无额外依赖），走 vite 已配的 /ws proxy。
 * 订阅 incident 变更，收到推送后由调用方（hook）刷新 React Query 缓存。
 *
 * 连接管理：自动重连（指数退避，上限 30s），组件卸载时关闭。
 */
export interface WSMessage {
  type: "incident_changed" | "timeline_added";
  incident_id: number;
  action?: string;
  data?: unknown;
}

type MessageHandler = (msg: WSMessage) => void;

/**
 * subscribeIncident 订阅某 incident 的实时变更。
 * 返回 cleanup 函数（关闭连接，组件卸载时调用）。
 *
 * @param incidentId 订阅的 incident ID
 * @param onMessage 收到消息的回调
 * @returns cleanup 函数
 */
export function subscribeIncident(incidentId: number, onMessage: MessageHandler): () => void {
  let ws: WebSocket | null = null;
  let closed = false; // 主动关闭标志（避免重连）
  let reconnectDelay = 1000;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  const connect = () => {
    if (closed) return;
    // 构造 ws URL：同源（生产同源 / 开发 vite proxy /ws）
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${window.location.host}/ws/incidents/${incidentId}`;
    ws = new WebSocket(url);

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
