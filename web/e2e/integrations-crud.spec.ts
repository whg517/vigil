/**
 * integrations-crud.spec —— 接入点创建 + token 展示 + 启停（P2）。
 *
 * 覆盖：
 *   - 创建接入点（表单 + webhook URL + 一次性 token 二次展示）
 *   - 启停切换（行内按钮文案切换）
 *
 * token 一次性展示是易回归点（后端字段缺 tag 会导致 token undefined）。
 */
import { test, expect } from "./fixtures";
import { login, seedTeam } from "./api-client";

test.describe("接入点 CRUD", () => {
  test("创建接入点 → 展示 webhook URL + token", async ({ authedPage }) => {
    const token = await login();
    await seedTeam(token, "支付");

    await authedPage.goto("/integrations");
    await expect(authedPage.getByRole("heading", { name: "接入管理" })).toBeVisible();

    // 打开创建 Dialog（Dialog 无 role，用标题 h2 定位）
    await authedPage.getByRole("button", { name: "创建接入点" }).click();
    await expect(authedPage.getByRole("heading", { name: "创建接入点" })).toBeVisible();

    // 填名称（placeholder 定位）
    await authedPage.getByPlaceholder("prod-prometheus").fill("e2e-integ");

    // 提交
    await authedPage.getByRole("button", { name: "创建", exact: true }).click();

    // 创建成功 → 二次态展示 webhook URL + token
    await expect(authedPage.getByRole("heading", { name: "接入点已创建" })).toBeVisible({
      timeout: 10000,
    });
    // token 应是非空字符串（防 undefined 回归）
    await expect(authedPage.locator("code").last()).not.toBeEmpty({ timeout: 5000 });

    // 关闭二次态
    await authedPage.getByRole("button", { name: "我已保存" }).click();
    // 列表出现新接入点
    await expect(authedPage.getByText("e2e-integ")).toBeVisible({ timeout: 10000 });
  });

  test("启停切换：启用 → 停用", async ({ authedPage }) => {
    // 用 API 建接入点（聚焦启停交互）
    const token = await login();
    const team = await seedTeam(token, "运维");
    await createIntegrationViaApi(token, team.id);

    await authedPage.goto("/integrations");
    await expect(authedPage.getByText("e2e-toggle-integ")).toBeVisible({ timeout: 10000 });

    // 初始为「启用」状态，行内按钮显示「停用」（可能有多个接入点，定位到自己的行）
    const myRow = authedPage.locator("tr", { hasText: "e2e-toggle-integ" });
    const toggleBtn = myRow.getByRole("button", { name: "停用" });
    await expect(toggleBtn).toBeVisible();
    await toggleBtn.click();

    // 切换后按钮变「启用」（状态翻转）
    await expect(myRow.getByRole("button", { name: "启用" })).toBeVisible({ timeout: 10000 });
  });
});

async function createIntegrationViaApi(token: string, teamID: number): Promise<void> {
  const resp = await fetch("http://localhost:28080/api/v1/integrations", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ name: "e2e-toggle-integ", type: "prometheus", config: {}, team_id: teamID }),
  });
  if (!resp.ok) throw new Error(`create integ failed: ${resp.status}`);
}
