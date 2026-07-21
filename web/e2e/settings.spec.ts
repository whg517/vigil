/**
 * settings.spec —— 设置页 Tab 切换 + API Key 创建（P2）。
 *
 * 覆盖：
 *   - 5 个 tab 切换（权限/API Key/审计/通知/IM）—— 每个切换后内容区可见
 *   - API Key 创建 + 一次性 token 展示
 *
 * settings 是最大文件（5 tab），admin-pages 只测了默认 rbac 标题。
 */
import { test, expect } from "./fixtures";

const TABS = [
  { value: "权限（RBAC）", expect: "角色" },
  { value: "API Key", expect: "创建" },
  { value: "审计日志", expect: "审计" },
  { value: "通知配置", expect: /通知规则|通知模板|抑制/ },
  { value: "IM 平台", expect: /飞书|钉钉|IM/ },
];

test.describe("设置页", () => {
  for (const { value, expect: expectText } of TABS) {
    test(`切换到「${value}」tab → 内容区渲染`, async ({ authedPage }) => {
      await authedPage.goto("/settings");
      await expect(authedPage.getByRole("heading", { name: "设置" })).toBeVisible();

      // Tabs 是普通 button（非 role=tab），用 button 定位
      await authedPage.getByRole("button", { name: value, exact: false }).click();
      // 内容区应出现对应文本（验证 tab 切换生效，非空白）
      await expect(authedPage.getByText(expectText).first()).toBeVisible({ timeout: 10000 });
    });
  }

  test("创建 API Key → 展示一次性 token", async ({ authedPage }) => {
    await authedPage.goto("/settings");
    await authedPage.getByRole("button", { name: "API Key" }).click();

    // API Key tab 的创建按钮（trigger，i18n common.create=新建）
    await authedPage.getByRole("button", { name: "新建" }).first().click();
    await expect(authedPage.getByRole("heading", { name: "创建 API Key" })).toBeVisible({
      timeout: 5000,
    });

    // 填名称（用 placeholder 精确定位，避免匹配到有效期 number input）
    await authedPage.getByPlaceholder("ci-deploy-key").fill("e2e-key");
    // Dialog 内的提交按钮（i18n common.create=新建；用 form 定位避免匹配 RBAC 的创建按钮）
    await authedPage.locator("form").getByRole("button", { name: "新建", exact: true }).click();

    // 应展示一次性 token（"API Key 已创建"二次态）
    await expect(authedPage.getByRole("heading", { name: "API Key 已创建" })).toBeVisible({
      timeout: 10000,
    });
  });
});
