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

// 每个测试前 resetDB（隔离）+ 等待 asynq worker 收尾。
// 注意：必须注册在 test（而非 base）上——extend 后的 test 不继承 base 的 beforeEach。
// login.spec 直接用 @playwright/test，不依赖业务数据，无需 reset。
// 等待原因：reset 清表 + 清 asynq 任务，但 worker 可能已 pick up 前序测试的
// in-flight 任务，这些任务在 reset 后落库会污染当前测试（尤其"空数据"类断言）。
// 1.5s 覆盖 normalize+triage 的处理窗口。
test.beforeEach(async () => {
  await resetDB();
  await new Promise((r) => setTimeout(r, 1500));
});

export { expect };
