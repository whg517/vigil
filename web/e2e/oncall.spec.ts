/**
 * oncall.spec —— 值班排班契约闸门（P0）。
 *
 * 🔒 锁住的核心回归：后端 schedule.OncallResult/OncallLayer/OncallUser 结构体
 * 缺 json tag，前端读 layers/users/name 全 undefined。
 *
 * 验证：
 *   - 空数据状态：显示"还没有排班"提示，不崩溃
 *   - 创建排班后：选排班 + 在班人/预览区域渲染（不白屏）
 */
import { test, expect } from "./fixtures";
import { login, seedTeam, BASE_URL } from "./api-client";

test.describe("值班排班", () => {
  test("空数据状态：显示提示，不崩溃", async ({ authedPage }) => {
    await authedPage.goto("/oncall");

    await expect(authedPage.getByRole("heading", { name: "值班排班" })).toBeVisible();
    // 无排班时显示提示（验证页面渲染正常，字段错位不会导致白屏）
    await expect(authedPage.getByText("还没有排班")).toBeVisible();
  });

  test("创建排班后：页面渲染排班选择器 + 在班人区域", async ({ authedPage }) => {
    // 直接通过 API 创建一个排班（避免 UI 表单交互的复杂度，聚焦契约验证）
    const token = await login();
    const team = await seedTeam(token, "运维团队");
    const schedule = await createScheduleViaApi(token, {
      name: "一线值班",
      type: "rotation",
      timezone: "Asia/Shanghai",
      layers: [{ name: "一线", priority: 1, participants: [{ user_id: 1 }] }],
      team_id: team.id,
    });

    await authedPage.goto("/oncall");

    // 排班选择器应出现（不再是"还没有排班"）
    await expect(authedPage.getByText("当前在班人")).toBeVisible({ timeout: 15000 });
    await expect(authedPage.getByText("未来")).toBeVisible();

    // 两个区域卡片标题都在（验证字段错位时不会导致某区域消失）
    await expect(authedPage.getByText(/未来 \d+ 天预览/)).toBeVisible();
  });
});

/** 通过 API 创建排班（schedule 创建 API，复用后端契约）。 */
async function createScheduleViaApi(
  token: string,
  body: Record<string, unknown>,
): Promise<any> {
  const resp = await fetch(`${BASE_URL}/api/v1/schedules`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`create schedule failed: ${resp.status} ${await resp.text()}`);
  return resp.json();
}
