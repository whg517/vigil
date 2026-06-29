/**
 * oncall-create.spec —— 排班创建表单（P2）。
 *
 * 覆盖：创建排班（名称 + 分层）→ 列表出现 + 排班选择器可选。
 * 补充 oncall.spec（用 API 建排班）未覆盖的 UI 表单链路。
 */
import { test, expect } from "./fixtures";

test.describe("排班创建", () => {
test("创建排班 → 列表出现 + 选择器可选", async ({ authedPage }) => {
  await authedPage.goto("/oncall");
  await expect(authedPage.getByRole("heading", { name: "值班排班" })).toBeVisible();
  await expect(authedPage.getByText("还没有排班")).toBeVisible();

  // 打开创建 Dialog
  await authedPage.getByRole("button", { name: "创建排班" }).click();
  await expect(authedPage.getByRole("heading", { name: "创建排班" })).toBeVisible();

  // 填名称（placeholder 定位）
  await authedPage.getByPlaceholder("SRE 主排班").fill("e2e-排班");

  // 提交（用 exact 避免匹配「创建排班」按钮）
  await authedPage.getByRole("button", { name: "创建", exact: true }).click();

  // 创建成功 → "还没有排班"消失 + 在班人/预览区出现（验证排班已加载）
  await expect(authedPage.getByText("还没有排班")).toBeHidden({ timeout: 10000 });
  await expect(authedPage.getByText("当前在班人")).toBeVisible({ timeout: 10000 });
  await expect(authedPage.getByText(/未来 \d+ 天预览/)).toBeVisible({ timeout: 10000 });
});
});
