/**
 * users-teams-crud.spec —— 用户与团队：tab 切换 + 团队 CRUD（P2）。
 *
 * 覆盖（admin-pages 只测了默认 users tab 标题）：
 *   - users ↔ teams tab 切换
 *   - 创建团队（表单）
 *   - 删除团队
 */
import { test, expect } from "./fixtures";

test.describe("用户与团队", () => {
  test("切到「团队」tab → 内容区渲染", async ({ authedPage }) => {
    await authedPage.goto("/users-teams");
    await expect(authedPage.getByRole("heading", { name: "用户与团队" })).toBeVisible();

    // 点「团队」tab（普通 button）
    await authedPage.getByRole("button", { name: "团队" }).click();
    // 团队区出现「创建团队」按钮
    await expect(authedPage.getByRole("button", { name: "创建团队" })).toBeVisible({ timeout: 5000 });
  });

  // TODO(隔离): 创建/删除后列表刷新受 staleTime 缓存 + 测试间污染影响，待启用。
  test.skip("创建团队 → 列表出现", async ({ authedPage }) => {
    await authedPage.goto("/users-teams");
    await authedPage.getByRole("button", { name: "团队" }).click();
    await expect(authedPage.getByRole("button", { name: "创建团队" })).toBeVisible();

    await authedPage.getByRole("button", { name: "创建团队" }).click();
    // Dialog 出现（标题含"创建团队"）
    await expect(authedPage.getByRole("heading", { name: "创建团队" })).toBeVisible({ timeout: 5000 });

    // 填名称 + slug（都是 required，否则创建按钮 disabled）
    await authedPage.getByPlaceholder("SRE 平台组").fill("e2e-team");
    await authedPage.getByPlaceholder("sre-platform").fill(`e2e-team-${Date.now()}`);

    // 提交后等列表刷新（invalidateQueries refetch）
    const [resp] = await Promise.all([
      authedPage.waitForResponse((r) => r.url().includes("/teams") && r.status() === 200),
      authedPage.getByRole("button", { name: "创建", exact: true }).click(),
    ]);

    // 列表出现新团队
    await expect(authedPage.getByText("e2e-team")).toBeVisible({ timeout: 10000 });
  });

  // TODO(隔离): 同创建团队，全量跑时 staleTime + 污染，待启用。
  test.skip("删除团队 → 列表移除", async ({ authedPage }) => {
    // 先用 API 创建团队（聚焦删除交互）
    const { login, seedTeam } = await import("./api-client");
    const token = await login();
    const team = await seedTeam(token, "del-team");

    await authedPage.goto("/users-teams");
    await authedPage.getByRole("button", { name: "团队" }).click();
    await authedPage.waitForResponse((r) => r.url().includes("/teams") && r.status() === 200);
    await expect(authedPage.getByText(team.name).first()).toBeVisible({ timeout: 10000 });

    // 注册 confirm 处理（删除调 window.confirm）
    authedPage.on("dialog", (d) => d.accept());

    // 点删除（title="删除" icon button）
    await authedPage.locator("tr", { hasText: team.name }).getByTitle("删除").click();
    // 团队从列表消失
    await expect(authedPage.getByText(team.name)).toBeHidden({ timeout: 10000 });
  });
});
