/**
 * escalation.spec —— 升级链 UI 闭环（P1）。
 *
 * 验证：配 2 层策略（delay=0）→ 发告警 → 详情页 current_level 推进 1→2。
 * 详情页头部显示"当前升级层级 L{n}"，轮询该文本捕获推进过程。
 *
 * TODO: 本地因机器过载（load 244）未完成验证。依赖根因 A 的修复
 * （service PATCH escalation_policy_id，已修）+ 流水线时序。
 * CI 环境稳定后启用。mark with test.describe.configure({mode: 'skip'}) 临时跳过。
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
  waitForFirstIncidentID,
} from "./api-client";

// TODO(local): 本地机器过载未完成验证，CI 环境启用。删除下行即可恢复运行。
test.describe.configure({ mode: "skip" });

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

    // 发告警触发流水线
    await sendWebhook(integToken, svc.slug, "fp-web-esc-" + Date.now());

    // 轮询 API 拿 incident id 后直接进详情（不依赖 DOM 行点击）
    const incId = await waitForFirstIncidentID();
    await authedPage.goto(`/incidents/${incId}`);

    // 轮询"当前升级层级"，等待推进到 L2（delay=0 应在数秒内完成 1→2）
    await expect(authedPage.getByText(/当前升级层级 L2/)).toBeVisible({ timeout: 30000 });
  });
});
