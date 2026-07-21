/**
 * services-crud.spec —— 服务创建表单交互（P2）。
 *
 * 代表管理页 CRUD 表单链路（Dialog + Input + 提交 + React Query 刷新）。
 * 覆盖：打开创建 Dialog → 填表 → 提交 → 列表新增行。
 *
 * 注意：Dialog 组件（components/ui/dialog）是轻量自实现，无 role="dialog"，
 * 故用标题文本「创建服务」的 h2 定位表单容器，用 Field label 文本定位 input。
 */
import { test, expect } from "./fixtures";
import { login, seedTeam } from "./api-client";

test.describe("服务 CRUD", () => {
  test("创建服务 → 列表新增", async ({ authedPage }) => {
    // 先建团队（服务创建需选团队）
    const token = await login();
    await seedTeam(token, "测试团队");

    await authedPage.goto("/services");
    await expect(authedPage.getByRole("heading", { name: "服务" })).toBeVisible();

    // 打开创建 Dialog
    await authedPage.getByRole("button", { name: "创建服务" }).click();

    // Dialog 标题 h2「创建服务」可见即表单已展开
    const dialogTitle = authedPage.getByRole("heading", { name: "创建服务" }).last();
    await expect(dialogTitle).toBeVisible();

    // 用 placeholder 精确定位输入框（exact 避免匹配多元素）
    await authedPage.getByPlaceholder("payment-api", { exact: true }).fill("e2e-test-service");
    await authedPage.getByPlaceholder("payment", { exact: true }).fill(`e2e-slug-${Date.now()}`);

    // 提交按钮文案「新建」（i18n common.create=新建）
    await authedPage.getByRole("button", { name: "新建", exact: true }).click();

    // 列表新增行（等待新服务名出现，Dialog 关闭）
    await expect(authedPage.getByText("e2e-test-service").first()).toBeVisible({
      timeout: 15000,
    });
  });
});
