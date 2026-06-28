/**
 * fixtures —— 扩展 Playwright test，注入 e2e 专用能力。
 *
 * - 每个测试前自动 resetDB（保证用例隔离）
 * - 提供 authedPage：已注入登录态的 page（跳过登录流程）
 *
 * 用法：spec 内 `import { test, expect } from "./fixtures"` 替代直接 import @playwright/test。
 * login.spec 例外（它直接用 @playwright/test 测登录流本身）。
 */
import { test as base, expect } from "@playwright/test";
import { resetDB } from "./api-client";

// storageState 文件由 globalSetup 写入（admin 登录态）。
const STORAGE_STATE = "./e2e/.auth/admin.json";

/**
 * authedPage —— 已注入登录态的 page。
 * 大多数 spec 用这个（跳过登录流程，直接测业务页面）。
 */
export const test = base.extend<{ authedPage: import("@playwright/test").Page }>({
  authedPage: async ({ browser }, use) => {
    const ctx = await browser.newContext({ storageState: STORAGE_STATE });
    const page = await ctx.newPage();
    await use(page);
    await ctx.close();
  },
});

// 每个测试前 resetDB（隔离）。所有 spec 自动生效。
base.beforeEach(async () => {
  await resetDB();
});

export { expect };
