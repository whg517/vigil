/**
 * incident-detail-extras.spec —— 事件详情补充交互（P1）。
 *
 * 覆盖现有 incident-action.spec 未覆盖的：
 *   - 「升级」按钮点击（action.escalate，事件状态机第 4 个按钮）
 *   - AI 诊断链路：e2e 无 LLM → 点「诊断」显示降级提示
 *   - 相似事件切换（懒加载空状态）
 *   - 「返回列表」导航
 */
import { test, expect } from "./fixtures";
import {
  login,
  seedTeam,
  seedService,
  seedIntegration,
  sendWebhook,
  waitForNewIncidentID,
} from "./api-client";

async function setupIncident(page: import("@playwright/test").Page) {
  const token = await login();
  const before = await fetch("http://localhost:28080/api/v1/incidents?limit=1", {
    headers: { Authorization: `Bearer ${token}` },
  }).then((r) => r.json());
  const beforeCount = before.total ?? 0;
  const team = await seedTeam(token, "支付");
  const svc = await seedService(token, "pay-api", team.id);
  const { token: integToken } = await seedIntegration(token, "prometheus", team.id, svc.id);
  await sendWebhook(integToken, svc.slug, "fp-detail-" + Date.now());
  const id = await waitForNewIncidentID(token, beforeCount);
  await page.goto(`/incidents/${id}`);
  await expect(page.locator("h1").first()).toBeVisible();
  return { id, token };
}

test.describe("事件详情 - 升级操作", () => {
  test("点击「升级」→ 状态推进（current_level 变化）", async ({ authedPage }) => {
    const { token } = await setupIncident(authedPage);

    // 记录初始 current_level（用 API 查，避免依赖文案）
    const before = await fetch(`http://localhost:28080/api/v1/incidents/${authedPage.url().match(/\/incidents\/(\d+)/)?.[1]}`, {
      headers: { Authorization: `Bearer ${token}` },
    }).then((r) => r.json());
    const beforeLevel = before.current_level ?? 0;

    // 点「升级」按钮（手动触发立即升级）
    await authedPage.getByRole("button", { name: "升级" }).click();

    // 轮询 API 确认 current_level 推进（≥ beforeLevel+1）
    await expect.poll(
      async () => {
        const inc = await fetch(`http://localhost:28080/api/v1/incidents/${authedPage.url().match(/\/incidents\/(\d+)/)?.[1]}`, {
          headers: { Authorization: `Bearer ${token}` },
        }).then((r) => r.json());
        return inc.current_level ?? 0;
      },
      { timeout: 15000, message: "升级后 current_level 应推进" },
    ).toBeGreaterThan(beforeLevel);
  });
});

test.describe("事件详情 - AI 诊断", () => {
  test("无 LLM 时点「诊断」→ 显示降级提示", async ({ authedPage }) => {
    await setupIncident(authedPage);

    // 点「诊断」按钮（页面内唯一）
    await authedPage.getByRole("button", { name: "诊断", exact: true }).click();

    // e2e 环境未配 LLM，应显示降级提示文案（提示 + toast，取 first）
    await expect(authedPage.getByText("AI 诊断未启用").first()).toBeVisible({ timeout: 10000 });
  });

  test("「相似事件」切换 → 显示区域", async ({ authedPage }) => {
    await setupIncident(authedPage);

    // 点「相似事件」按钮
    await authedPage.getByRole("button", { name: "相似事件" }).click();

    // 应出现「相似历史事件」区域
    await expect(authedPage.getByText("相似历史事件")).toBeVisible({ timeout: 10000 });

    // 再点「隐藏相似事件」收起
    await authedPage.getByRole("button", { name: "隐藏相似事件" }).click();
  });
});

test.describe("事件详情 - 导航", () => {
  test("「返回列表」→ 跳回 /incidents", async ({ authedPage }) => {
    await setupIncident(authedPage);

    await authedPage.getByText("返回列表").click();
    await expect(authedPage).toHaveURL(/\/incidents$/);
  });
});
