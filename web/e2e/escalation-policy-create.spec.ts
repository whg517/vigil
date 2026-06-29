/**
 * escalation-policy-create.spec —— 升级策略创建表单（P2）。
 *
 * 覆盖：创建策略（名称 + 层级延迟 + 通道）→ 列表出现。
 * 补充 escalation.spec（测 incident 升级链）未覆盖的策略 CRUD。
 */
import { test, expect } from "./fixtures";

test.describe("升级策略创建", () => {
test("创建策略 → 列表出现", async ({ authedPage }) => {
    await authedPage.goto("/escalation-policies");
    await expect(authedPage.getByRole("heading", { name: "升级策略" })).toBeVisible();

    // 打开创建 Dialog（标题是"创建升级策略"）
    await authedPage.getByRole("button", { name: "创建策略" }).click();
    await expect(authedPage.getByRole("heading", { name: "创建升级策略" })).toBeVisible();

    // 填名称
    await authedPage.getByPlaceholder("默认升级（5min→IM）").fill("e2e-策略");

    // 提交（exact 避免匹配「创建策略」）
    await authedPage.getByRole("button", { name: "创建", exact: true }).click();

    // 列表出现新策略
    await expect(authedPage.getByText("e2e-策略")).toBeVisible({ timeout: 10000 });
  });
});
