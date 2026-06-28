/**
 * globalTeardown —— 关停 Docker 全栈。
 *
 * 与 globalSetup 对应。用 down -v 清理容器 + 数据卷（e2e 数据用完即弃）。
 * 默认保留镜像（避免下次构建从零开始）；如需彻底清理手动 docker compose down --rmi。
 */
import { execSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..", "..");
const COMPOSE_FILE = path.join(REPO_ROOT, "docker-compose.e2e.yml");

export default async function globalTeardown() {
  // 跳过模式（VIGIL_E2E_SKIP_SETUP=1）下不关停服务（服务是手动起的，由调用方管理）。
  if (process.env.VIGIL_E2E_SKIP_SETUP === "1") {
    console.log("\n[e2e] 跳过 compose 关停（VIGIL_E2E_SKIP_SETUP=1）");
    return;
  }
  console.log("\n[e2e] 关停 Docker 全栈...");
  try {
    execSync(`docker compose -f ${COMPOSE_FILE} down -v`, { stdio: "inherit" });
    console.log("[e2e] 全栈已关停");
  } catch (err) {
    // teardown 失败不应让整个测试 run 失败（仅记录）。
    console.error("[e2e] 关停失败（不影响测试结果）:", err);
  }
}
