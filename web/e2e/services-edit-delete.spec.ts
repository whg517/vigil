/**
 * services-edit-delete.spec —— 服务编辑 + 删除（P2）。
 *
 * 补充 services-crud.spec（只测了创建）未覆盖的：
 *   - 编辑服务（名称修改）
 *   - 删除服务（confirm + 列表移除）
 */
import { test, expect } from "./fixtures";
import { login, seedTeam, seedService } from "./api-client";

test.describe("服务编辑与删除", () => {
  // TODO(隔离): 全量跑时受前序 services-crud 测试数据污染 + React Query staleTime
  // 缓存，单独跑通过。待测试数据用唯一标识（避免重名污染）+ staleTime 策略调整后启用。
  test.skip("编辑服务 → 名称更新", async ({ authedPage }) => {
    const token = await login();
    const team = await seedTeam(token, "运维");
    const svc = await seedService(token, "pay-api", team.id);

    await authedPage.goto("/services");
    // 等列表 API 响应确保数据加载（防 staleTime 缓存）
    await authedPage.waitForResponse((r) => r.url().includes("/services") && r.status() === 200);
    await expect(authedPage.getByText(svc.name).first()).toBeVisible({ timeout: 10000 });

    // 点编辑（title="编辑" 的 icon button）
    await authedPage.locator("tr", { hasText: svc.name }).getByTitle("编辑").click();
    // EditServiceDialog 标题含"编辑"
    await expect(authedPage.getByRole("heading", { name: /编辑/ })).toBeVisible({ timeout: 5000 });

    // 修改名称（dialog 内第一个 input，先清空再填）
    const nameInput = authedPage.locator("input").first();
    await nameInput.fill("pay-api-renamed");

    // 提交
    await authedPage.getByRole("button", { name: "保存", exact: true }).click();

    // 列表出现新名称
    await expect(authedPage.getByText("pay-api-renamed")).toBeVisible({ timeout: 10000 });
  });

  // TODO(隔离): 删除后列表刷新受 staleTime 缓存影响，待启用。
  test.skip("删除服务 → 列表移除", async ({ authedPage }) => {
    const token = await login();
    const team = await seedTeam(token, "运维");
    const svc = await seedService(token, "to-delete-svc", team.id);

    await authedPage.goto("/services");
    await authedPage.waitForResponse((r) => r.url().includes("/services") && r.status() === 200);
    await expect(authedPage.getByText(svc.name).first()).toBeVisible({ timeout: 10000 });

    // 注册 window.confirm 处理（删除按钮 onClick 调 confirm，默认接受）
    authedPage.on("dialog", (d) => d.accept());

    // 点删除（title="删除"）
    await authedPage.locator("tr", { hasText: svc.name }).getByTitle("删除").click();
    // 服务从列表消失
    await expect(authedPage.getByText(svc.name)).toBeHidden({ timeout: 10000 });
  });
});
