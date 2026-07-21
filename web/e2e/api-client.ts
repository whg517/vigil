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

// ADMIN 密码：e2e 全栈专用（每次 compose up 是全新库，密码可硬编码）。
// globalSetup 用 changeme 首登 → 改密到此值（清 MustChangePassword 标志），
// 之后所有 spec 调 login() 默认用此值。原 changeme 被 MustChangePassword 拦截器封锁。
export const ADMIN = { username: "admin", password: "e2e-test-password-12345" } as const;
// 首登原始密码（仅 globalSetup 改密时用一次）。
export const ADMIN_BOOTSTRAP_PASSWORD = "changeme";

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

/** 登录拿 access token（默认用 ADMIN：globalSetup 已改密清 MustChangePassword）。 */
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

/** changePassword 改密（清 MustChangePassword 标志）。
 *  改密后旧 token 立即失效（后端 AddTokenVersion(1)），调用方需重新 login。 */
export async function changePassword(token: string, oldPassword: string, newPassword: string): Promise<void> {
  await req("/auth/change-password", {
    method: "POST",
    token,
    body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
  });
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

/** seedSchedule 创建排班（含分层），返回排班对象。 */
export async function seedSchedule(
  token: string,
  name: string,
  teamID: number,
  layers: { name: string; priority: number; participants: { user_id: number }[] }[] = [],
): Promise<any> {
  return req("/schedules", {
    method: "POST",
    token,
    body: JSON.stringify({
      name,
      type: "rotation",
      timezone: "Asia/Shanghai",
      layers,
      team_id: teamID,
    }),
  });
}

/** seedRunbook 创建 Runbook（可执行式），返回对象。 */
export async function seedRunbook(token: string, name: string, type: "executable" | "document" = "executable"): Promise<any> {
  return req("/runbooks", {
    method: "POST",
    token,
    body: JSON.stringify({
      name,
      type,
      content_markdown: "# 处置步骤\n1. 检查服务状态\n2. 重启服务",
    }),
  });
}

/** seedPostmortemDraft 从 incident 起草复盘，返回复盘对象。 */
export async function seedPostmortemDraft(token: string, incidentId: number): Promise<any> {
  return req(`/incidents/${incidentId}/postmortem/draft`, {
    method: "POST",
    token,
    body: "{}",
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
 *
 *  注意：/incidents 受 RequireUser 保护，必须传 token。
 */
export async function waitForFirstIncidentID(token: string, timeoutMs = 30000): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const data = await req("/incidents?limit=1", { token });
      const items = data?.items ?? [];
      if (items.length > 0) return items[0].id as number;
    } catch {
      // 忽略，重试
    }
    await sleep(300);
  }
  throw new Error(`等待 incident 创建超时（${timeoutMs}ms）`);
}

/** waitForNewIncidentID 等待「比 beforeCount 更多」的 incident 出现，返回新增的 id。
 *  用于隔离场景：reset 后若有残留，用 beforeCount 区分自己造的新 incident。
 */
export async function waitForNewIncidentID(
  token: string,
  beforeCount: number,
  timeoutMs = 30000,
): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const data = await req("/incidents?limit=50", { token });
      const items = data?.items ?? [];
      if (items.length > beforeCount) {
        // 返回 id 最小的「新增」项（items 按 id 降序或升序，取 last 确保是新创建的）。
        // items[0] 通常是最新的（后端默认按 created_at desc）。
        return items[0].id as number;
      }
    } catch {
      // 忽略，重试
    }
    await sleep(300);
  }
  throw new Error(`等待新 incident 创建超时（before=${beforeCount}, ${timeoutMs}ms）`);
}

function sleep(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}

/** slugify：名字转 slug（加时间戳后缀保证唯一）。 */
function slugify(name: string): string {
  return `${name}-${Date.now()}`;
}
