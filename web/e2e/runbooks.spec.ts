/**
 * runbooks.spec —— Runbook 创建 + 执行（human-in-the-loop）（P1）。
 *
 * 覆盖：
 *   - 创建 Runbook（Markdown Textarea 表单）
 *   - 列表 → 详情导航（卡片点击）
 *   - 执行 Runbook（human-in-the-loop「确认执行」）
 *
 * Runbook 是告警处置的核心写操作，human-in-the-loop 确认是设计基线第 5 条。
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

test.describe("Runbook", () => {
  test("创建 Runbook → 列表出现 → 进详情", async ({ authedPage }) => {
    await authedPage.goto("/runbooks");
    await expect(authedPage.getByRole("heading", { name: "Runbook" })).toBeVisible();
    await expect(authedPage.getByText("还没有 Runbook")).toBeVisible();

    // 打开创建 Dialog
    await authedPage.getByRole("button", { name: "创建 Runbook" }).click();
    await expect(authedPage.getByRole("heading", { name: "创建 Runbook" })).toBeVisible();

    // 填表：名称（autofocus 的第一个 input）+ 内容
    await authedPage.getByText("名称").locator("..").locator("input").fill("e2e-runbook");

    // 提交
    await authedPage.getByRole("button", { name: "创建", exact: true }).click();

    // 列表出现新卡片
    await expect(authedPage.getByText("e2e-runbook")).toBeVisible({ timeout: 10000 });

    // 点击卡片进详情
    await authedPage.getByText("e2e-runbook").click();
    // 详情页有「执行」按钮
    await expect(authedPage.getByRole("button", { name: "执行" })).toBeVisible({ timeout: 10000 });
  });

  test("执行 Runbook（human-in-the-loop 确认）", async ({ authedPage }) => {
    // 造数据：先建 incident（执行 Runbook 需要 incidentId）
    const token = await login();
    const before = await fetch("http://localhost:28080/api/v1/incidents?limit=1", {
      headers: { Authorization: `Bearer ${token}` },
    }).then((r) => r.json());
    const team = await seedTeam(token, "支付");
    const svc = await seedService(token, "pay-api", team.id);
    const { token: integToken } = await seedIntegration(token, "prometheus", team.id, svc.id);
    await sendWebhook(integToken, svc.slug, "fp-rb-" + Date.now());
    const incId = await waitForNewIncidentID(token, before.total ?? 0);

    // 用 API 建 Runbook（聚焦执行交互，不重复测创建表单）
    const rb = await seedRunbookViaApi(token, "e2e-exec-rb");

    await authedPage.goto("/runbooks");
    await authedPage.getByText("e2e-exec-rb").click();
    await expect(authedPage.getByRole("button", { name: "执行" })).toBeVisible();

    // 点「执行」→ 弹确认 Dialog
    await authedPage.getByRole("button", { name: "执行" }).click();
    await expect(authedPage.getByRole("heading", { name: "执行 Runbook" })).toBeVisible();

    // 填 incident ID
    await authedPage.getByPlaceholder("42").fill(String(incId));

    // 点「确认执行」（human-in-the-loop）
    await authedPage.getByRole("button", { name: "确认执行" }).click();

    // Dialog 关闭 + 出现执行结果（toast 或结果文本）
    await expect(authedPage.getByRole("heading", { name: "执行 Runbook" })).toBeHidden({
      timeout: 15000,
    });
  });
});

/** 通过 API 创建 Runbook（聚焦执行交互时用，避免重复走创建表单）。 */
async function seedRunbookViaApi(token: string, name: string): Promise<any> {
  const resp = await fetch("http://localhost:28080/api/v1/runbooks", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({
      name,
      type: "executable",
      content_markdown: "# 步骤\n1. 检查",
    }),
  });
  if (!resp.ok) throw new Error(`create runbook failed: ${resp.status}`);
  return resp.json();
}
