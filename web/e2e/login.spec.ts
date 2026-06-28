/**
 * login.spec —— 登录契约闸门（P0）。
 *
 * 验证：用户名密码 → JWT → 存 localStorage → 跳 dashboard。
 * 锁住的回归：鉴权流 + token 持久化 + 路由守卫。
 *
 * 注意：本 spec 直接用 @playwright/test（不依赖 storageState），因为它测登录流本身。
 */
import { test, expect } from "@playwright/test";

test.describe("登录流程", () => {
  test("正确凭据登录 → 跳转 dashboard + token 持久化", async ({ page }) => {
    await page.goto("/login");

    // 登录页元素
    await expect(page.getByRole("heading", { name: "Vigil 登录" })).toBeVisible();
    await expect(page.getByPlaceholder("admin")).toBeVisible();

    // 填表登录
    await page.getByPlaceholder("admin").fill("admin");
    await page.getByPlaceholder("••••••").fill("changeme");
    await page.getByRole("button", { name: "登录" }).click();

    // 跳转到 dashboard（URL 变化 + dashboard 标题出现）
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole("heading", { name: "仪表盘" })).toBeVisible();

    // token 已存入 localStorage（前端 http.ts 拦截器读 vigil_token）
    const token = await page.evaluate(() => localStorage.getItem("vigil_token"));
    expect(token).toBeTruthy();
  });

  test("错误密码 → 不跳转 + 无 token", async ({ page }) => {
    await page.goto("/login");
    await page.getByPlaceholder("admin").fill("admin");
    await page.getByPlaceholder("••••••").fill("wrong-password");
    await page.getByRole("button", { name: "登录" }).click();

    // 仍停留在登录页
    await expect(page).toHaveURL(/\/login$/);
    // token 不应存在
    const token = await page.evaluate(() => localStorage.getItem("vigil_token"));
    expect(token).toBeNull();
  });

  test("未登录访问受保护页 → 重定向到 login", async ({ page }) => {
    // 清空可能残留的 token
    await page.goto("/login");
    await page.evaluate(() => localStorage.clear());

    // 直接访问 dashboard（受保护）
    await page.goto("/");
    await expect(page).toHaveURL(/\/login$/);
  });

  test("已登录访问 /login → 重定向到 dashboard", async ({ page }) => {
    // 先登录
    await page.goto("/login");
    await page.getByPlaceholder("admin").fill("admin");
    await page.getByPlaceholder("••••••").fill("changeme");
    await page.getByRole("button", { name: "登录" }).click();
    await expect(page.getByRole("heading", { name: "仪表盘" })).toBeVisible();

    // 再访问 /login 应跳走
    await page.goto("/login");
    await expect(page).toHaveURL(/\/$/);
  });
});
