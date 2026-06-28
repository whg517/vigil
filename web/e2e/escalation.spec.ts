/**
 * escalation.spec —— 升级链 UI 闭环（P1）。
 *
 * 验证：配 2 层策略（delay=0）→ 发告警 → 详情页 current_level 推进 1→2。
 * 详情页头部显示"当前升级层级 L{n}"，轮询该文本捕获推进过程。
 */
import { test, expect } from "./fixtures";
import {
  login,
  seedTeam,
  seedService,
  seedIntegration,
  seedEscalationPolicy,
  bindPolicyToService,
  sendWebhook,
  waitForNewIncidentID,
} from "./api-client";

// req 未从 api-client 导出（模块私有），这里内联一个轻量查询。
async function fetchIncident(token: string, id: number) {
  const resp = await fetch(`http://localhost:28080/api/v1/incidents/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return resp.json();
}

test.describe("升级链", () => {
  test("2 层 delay=0 策略 → current_level 推进到 L2", async ({ authedPage }) => {
    const token = await login();
    const team = await seedTeam(token, "运维");
    const svc = await seedService(token, "ops-svc", team.id);
    const { token: integToken } = await seedIntegration(token, "prometheus", team.id, svc.id);

    // 2 层策略，delay=0 立即触发
    const policy = await seedEscalationPolicy(token, "e2e-web-esc", [
      {
        delay_minutes: 0,
        targets: [{ type: "team", target_id: String(team.id) }],
        notify_channels: ["im"],
      },
      {
        delay_minutes: 0,
        targets: [{ type: "team", target_id: String(team.id) }],
        notify_channels: ["im"],
      },
    ]);
    await bindPolicyToService(token, svc.id, policy.id);

    // 造数据前记录现有 incident 数（防 waitForFirstIncidentID 拿到残留）。
    const beforeList = await fetch(
      `http://localhost:28080/api/v1/incidents?limit=1`,
      { headers: { Authorization: `Bearer ${token}` } },
    ).then((r) => r.json());
    const beforeCount = beforeList.total ?? 0;

    // 发告警触发流水线
    await sendWebhook(integToken, svc.slug, "fp-web-esc-" + Date.now());

    // 轮询 API 等「新增」的 incident（count > beforeCount），避免拿到残留。
    const incId = await waitForNewIncidentID(token, beforeCount);
    // 等升级链推进完成（delay=0，数秒内 0→1→2）。轮询 API 而非 UI，更可靠。
    await expect.poll(
      async () => {
        const inc = await fetchIncident(token, incId);
        return inc.current_level;
      },
      { timeout: 25000, message: "incident current_level 应推进到 2" },
    ).toBe(2);
    await authedPage.goto(`/incidents/${incId}`);

    // 轮询"当前升级层级"，等待推进到 L2（delay=0 应在数秒内完成 1→2）
    await expect(authedPage.getByText(/当前升级层级 L2/)).toBeVisible({ timeout: 10000 });
  });
});
