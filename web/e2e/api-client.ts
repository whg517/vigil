/**
 * api-client —— 测试用后端 API client（Node 端，不经浏览器）。
 *
 * 用途：spec 间 resetDB、造数据（team/service/incident）、登录拿 token。
 * 与前端 src/lib/api.ts 对应同一套后端 API，但走 fetch（Node 18+ 内建），
 * 避免 spec 内每个数据操作都走浏览器点击。
 */

const BASE_URL = process.env.VIGIL_E2E_BASE_URL ?? "http://localhost:28080";
const API_BASE = `${BASE_URL}/api/v1`;

/** 导出 BASE_URL 供 spec 内直接 fetch 复用（避免硬编码端口散落）。 */
export { BASE_URL };

export const ADMIN = { username: "admin", password: "changeme" } as const;

/** 通用请求：带可选 token，返回 JSON。 */
async function req(path: string, init: RequestInit & { token?: string } = {}): Promise<any> {
  const { token, ...rest } = init;
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(rest.headers as Record<string, string>),
  };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const resp = await fetch(`${API_BASE}${path}`, { ...rest, headers });
  const text = await resp.text();
  if (!resp.ok) {
    throw new Error(`${init.method ?? "GET"} ${path} → ${resp.status}: ${text}`);
  }
  return text ? JSON.parse(text) : null;
}

/** 登录拿 access token（admin/changeme）。 */
export async function login(creds = ADMIN): Promise<string> {
  const data = await req("/auth/login", {
    method: "POST",
    body: JSON.stringify(creds),
  });
  return data.access_token as string;
}

/** resetDB：清空业务表 + Redis（专用测试端点）。 */
export async function resetDB(): Promise<void> {
  await req("/__test__/reset", { method: "POST" });
}

/** seedTeam 创建团队。 */
export async function seedTeam(token: string, name: string): Promise<any> {
  return req("/teams", {
    method: "POST",
    token,
    body: JSON.stringify({ name, slug: slugify(name) }),
  });
}

/** seedService 创建服务并绑定团队，auto_create_incident=true。 */
export async function seedService(token: string, name: string, teamID: number): Promise<any> {
  return req("/services", {
    method: "POST",
    token,
    body: JSON.stringify({ name, slug: slugify(name), team_id: teamID, auto_create_incident: true }),
  });
}

/** seedIntegration 创建接入点，返回 { integration, token }。 */
export async function seedIntegration(
  token: string,
  kind: string,
  teamID: number,
  serviceID: number,
): Promise<{ integration: any; token: string }> {
  const data = await req("/integrations", {
    method: "POST",
    token,
    body: JSON.stringify({ name: `e2e-${kind}`, type: kind, config: {}, team_id: teamID, service_id: serviceID }),
  });
  return { integration: data, token: data.token as string };
}

/** seedEscalationPolicy 创建升级策略。 */
export async function seedEscalationPolicy(
  token: string,
  name: string,
  levels: { delay_minutes: number; targets: { type: string; target_id: string }[]; notify_channels: string[] }[],
): Promise<any> {
  const rawLevels = levels.map((lv, i) => ({ level: i + 1, ...lv }));
  return req("/escalation-policies", {
    method: "POST",
    token,
    body: JSON.stringify({ name, levels: rawLevels }),
  });
}

/** bindPolicyToService 把升级策略绑定到 service。 */
export async function bindPolicyToService(token: string, serviceID: number, policyID: number): Promise<void> {
  await req(`/services/${serviceID}`, {
    method: "PATCH",
    token,
    body: JSON.stringify({ escalation_policy_id: policyID }),
  });
}

/** 发送 Prometheus 格式告警到接入点，触发 ingestion→triage 流水线。 */
export async function sendWebhook(integToken: string, serviceSlug: string, fingerprint: string): Promise<void> {
  const payload = {
    alerts: [
      {
        status: "firing",
        labels: {
          alertname: "TestAlert",
          severity: "critical",
          instance: "test:8080",
          service: serviceSlug,
        },
        annotations: { summary: "测试告警" },
        fingerprint,
      },
    ],
  };
  const resp = await fetch(`${API_BASE}/webhook/${integToken}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!resp.ok) throw new Error(`webhook 发送失败: ${resp.status}`);
}

/** 轮询直到 DB 中有 count 条 incident（异步流水线建 incident 有延迟）。 */
export async function waitForIncidentCount(_count: number): Promise<void> {
  // 此函数仅占位；实际轮询由 spec 内通过 UI 或独立查询完成。
  // 保留导出便于未来扩展为 Node 端轮询。
}

/** 轮询直到 DB 中有 incident，返回其 id（异步流水线建 incident 有延迟）。
 *  供需要进详情页的 spec 使用，避免依赖 DOM 行点击导航（React Router onClick 在
 *  Playwright click 下偶发不触发，改用直接 goto 详情页更稳定）。
 */
export async function waitForFirstIncidentID(timeoutMs = 30000): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const data = await req("/incidents?limit=1");
      const items = data?.items ?? [];
      if (items.length > 0) return items[0].id as number;
    } catch {
      // 忽略，重试
    }
    await sleep(300);
  }
  throw new Error(`等待 incident 创建超时（${timeoutMs}ms）`);
}

function sleep(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}

/** slugify：名字转 slug（加时间戳后缀保证唯一）。 */
function slugify(name: string): string {
  return `${name}-${Date.now()}`;
}
