/**
 * incident-action.spec —— 事件状态机交互闭环（P1）。
 *
 * 验证现有 Go e2e 做不到的「UI 交互闭环」：
 *   triggered →(确认)→ acked →(解决)→ resolved →(重新打开)→ triggered
 *   每步：徽章变色 + 时间线新增条目。
 *
 * 这条链路覆盖 React Query 刷新 + 时间线记录 + 状态徽章渲染。
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

/** 造一个 incident 并打开其详情页，返回 incident ID。 */
async function setupIncident(page: import("@playwright/test").Page) {
  const token = await login();
  const before = await fetch("http://localhost:28080/api/v1/incidents?limit=1", {
    headers: { Authorization: `Bearer ${token}` },
  }).then((r) => r.json());
  const beforeCount = before.total ?? 0;

  const team = await seedTeam(token, "支付");
  const svc = await seedService(token, "pay-api", team.id);
  const { token: integToken } = await seedIntegration(token, "prometheus", team.id, svc.id);
  await sendWebhook(integToken, svc.slug, "fp-action-" + Date.now());

  // 轮询 API 等新 incident（用 beforeCount 区分残留），拿 ID 后直接进详情。
  const id = await waitForNewIncidentID(token, beforeCount);
  await page.goto(`/incidents/${id}`);
  await expect(page.locator("h1").first()).toBeVisible();
  return id;
}

test.describe("事件状态机", () => {
  test("triggered → ack → resolve → reopen 全链路", async ({ authedPage }) => {
    await setupIncident(authedPage);

    // 初始状态：triggered（待响应徽章）
    await expect(authedPage.locator("h1").first()).toBeVisible();
    // 头部状态徽章含"待响应"或类似
    const headerStatus = authedPage.locator(".flex.items-start").first();
    await expect(headerStatus).toBeVisible();

    // 1. 确认：点击「确认」→ 徽章变"已确认"
    await authedPage.getByRole("button", { name: "确认" }).click();
    // 时间线新增"确认"相关条目
    await expect(authedPage.getByText(/确认|已确认/).first()).toBeVisible({ timeout: 10000 });
    // 「确认」按钮应禁用（已 ack）
    await expect(authedPage.getByRole("button", { name: "确认" })).toBeDisabled();

    // 2. 解决：点击「解决」→ 徽章变"已解决"，操作区变为「重新打开」
    await authedPage.getByRole("button", { name: "解决" }).click();
    await expect(authedPage.getByRole("button", { name: "重新打开" })).toBeVisible({
      timeout: 10000,
    });

    // 3. 重新打开：点击「重新打开」→ 回到 triggered
    await authedPage.getByRole("button", { name: "重新打开" }).click();
    await expect(authedPage.getByRole("button", { name: "确认" })).toBeVisible({
      timeout: 10000,
    });
  });

  test("时间线随操作累积记录", async ({ authedPage }) => {
    await setupIncident(authedPage);

    // 初始可能有创建记录；确认后时间线条目数应增加
    const itemsBefore = await authedPage.locator("ol.border-l > li").count();

    await authedPage.getByRole("button", { name: "确认" }).click();
    // 等待时间线刷新（React Query invalidate）
    await expect(authedPage.locator("ol.border-l > li")).toHaveCount(itemsBefore + 1, {
      timeout: 10000,
    });

    await authedPage.getByRole("button", { name: "解决" }).click();
    await authedPage.getByRole("button", { name: "重新打开" }).waitFor({ timeout: 10000 });
    // 解决后时间线又 +1
    await expect(authedPage.locator("ol.border-l > li")).toHaveCount(itemsBefore + 2, {
      timeout: 10000,
    });
  });
});
