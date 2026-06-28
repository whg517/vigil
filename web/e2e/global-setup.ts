/**
 * globalSetup —— 启动 Docker 全栈 + 登录 + 持久化登录态。
 *
 * 流程：
 *   1. docker compose -f docker-compose.e2e.yml up -d --build --wait
 *      （build vigil 镜像含前端 embed，wait 等所有 healthcheck 绿）
 *   2. 轮询 /health 确认就绪（compose --wait 已等，这里双保险）
 *   3. 登录 admin 拿 token，写入 storageState（供所有 spec 复用，免重复登录）
 *
 * globalTeardown 负责 docker compose down。
 *
 * 路径约定：docker-compose.e2e.yml 在仓库根（web/ 的上一级）。
 * playwright 从 web/ 目录运行，REPO_ROOT 解析为 ../。
 */
import { execSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { login } from "./api-client";

// ESM 下 __dirname 不存在，用 import.meta.url 构造等价路径。
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..", "..");
const COMPOSE_FILE = path.join(REPO_ROOT, "docker-compose.e2e.yml");
const AUTH_DIR = path.join(__dirname, ".auth");
const STORAGE_STATE = path.join(AUTH_DIR, "admin.json");
const BASE_URL = process.env.VIGIL_E2E_BASE_URL ?? "http://localhost:28080";

export default async function globalSetup() {
  // VIGIL_E2E_SKIP_SETUP=1 时跳过 compose 启动（服务已手动起好，仅登录 + 写 storageState）。
  // 用于本地调试：手动 docker compose up 后直接跑 spec，避免每次重建镜像。
  const skipCompose = process.env.VIGIL_E2E_SKIP_SETUP === "1";

  if (!skipCompose) {
    console.log("\n[e2e] 启动 Docker 全栈（postgres + redis + vigil）...");
    // --build：每次重建 vigil 镜像（捕获代码变更）；--wait：等 healthcheck 全绿。
    execSync(`docker compose -f ${COMPOSE_FILE} up -d --build --wait`, { stdio: "inherit" });
  } else {
    console.log("\n[e2e] 跳过 compose（VIGIL_E2E_SKIP_SETUP=1），连接已起的服务");
  }

  // 双保险：再轮询 /health（compose --wait 偶有端口映射未就绪的边缘情况）。
  console.log("[e2e] 轮询 /health...");
  await pollHealth();

  mkdirSync(AUTH_DIR, { recursive: true });
  console.log("[e2e] 登录 admin 并持久化登录态...");
  const token = await login();
  // localStorage 内容（前端 http.ts 读 vigil_token / vigil_user_id）。
  const storageState = {
    cookies: [],
    origins: [
      {
        origin: BASE_URL,
        localStorage: [
          { name: "vigil_token", value: token },
          { name: "vigil_user_id", value: "1" },
        ],
      },
    ],
  };
  writeFileSync(STORAGE_STATE, JSON.stringify(storageState, null, 2));
  console.log("[e2e] 全栈就绪，storageState 已写入\n");
}

async function pollHealth(timeoutMs = 60_000, intervalMs = 500): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const resp = await fetch(`${BASE_URL}/health`);
      if (resp.ok) {
        const body = await resp.json();
        if (body.checks?.postgres === "up" && body.checks?.redis === "up") return;
      }
    } catch {
      // 服务未就绪，继续重试
    }
    await sleep(intervalMs);
  }
  throw new Error(`全栈 ${timeoutMs}ms 内未就绪`);
}

function sleep(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}
