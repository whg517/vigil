/**
 * Playwright 配置 —— 全栈 e2e（Docker Compose 真实环境，禁 mock）。
 *
 * 编排：globalSetup 起 docker-compose.e2e.yml 全栈（postgres + redis + vigil，
 * vigil 镜像含前端 embed），跑 migrate + 轮询 /health 就绪；globalTeardown 关停。
 * 测试间隔离：每个 spec 前 resetDB（专用 /__test__/reset 端点）。
 *
 * 认证：admin 登录态通过 storageState 复用，spec 内 page 已带 token，
 * 无需每个用例重复登录（login.spec 单独测登录流，不依赖 storageState）。
 */
import { defineConfig, devices } from "@playwright/test";

const BASE_URL = process.env.VIGIL_E2E_BASE_URL ?? "http://localhost:28080";
// 镜像与编排复用生产 Dockerfile（含前端 embed）。
const COMPOSE_FILE = "docker-compose.e2e.yml";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false, // 全栈单实例 + 共享 DB，串行避免 reset 竞态
  forbidOnly: !!process.env.CI,
  // QA 审计 Flaky 治理：CI 重试会掩盖 flaky（失败重试通过即绿）。
  // 改为 0 重试，失败即暴露；flaky 用例需根治而非靠重试洗绿。
  retries: 0,
  workers: 1, // 单实例必须单 worker
  reporter: process.env.CI ? "github" : "list",
  timeout: 60_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: BASE_URL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    // actionTimeout 30s：首次访问某页时前端 chunk 加载较慢（CI runner 共享资源），
    // 15s 在 GitHub Actions 上偶发超时找不到按钮。30s 给足余量，真实回归仍能捕获。
    actionTimeout: 30_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  // globalSetup/Teardown 负责 Docker 全栈生命周期。
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
  // 注入 compose 文件名供 setup/teardown 读取（避免硬编码重复）。
  metadata: { composeFile: COMPOSE_FILE },
});
