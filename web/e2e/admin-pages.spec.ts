/**
 * admin-pages.spec —— 管理页渲染闸门（P2）。
 *
 * 验证所有管理页打开不崩溃 + 标题渲染（覆盖路由 + 页面加载 + API 调用链路）。
 * 管理页 CRUD 的表单交互由 services-crud.spec 代表（Dialog 组件链路一致）。
 */
import { test, expect } from "./fixtures";

const PAGES = [
  { path: "/services", title: "服务" },
  { path: "/integrations", title: "接入管理" },
  { path: "/escalation-policies", title: "升级策略" },
  { path: "/users-teams", title: /用户|团队/ },
  { path: "/runbooks", title: "Runbook" },
  { path: "/postmortems", title: "复盘" },
  { path: "/settings", title: "设置" },
];

test.describe("管理页渲染闸门", () => {
  for (const { path, title } of PAGES) {
    test(`${path} 打开不崩溃 + 标题可见`, async ({ authedPage }) => {
      await authedPage.goto(path);
      await expect(authedPage.getByRole("heading", { name: title })).toBeVisible({
        timeout: 10000,
      });
    });
  }
});
