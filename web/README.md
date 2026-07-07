# Vigil Web（前端）

React 19 + TypeScript + Vite + Tailwind v4 + shadcn/ui。项目级约定见仓库根
[`AGENTS.md`](../AGENTS.md) 与 [`docs/`](../docs)。

## 国际化（i18n）

前端文案通过 [i18next](https://www.i18next.com/) + [react-i18next](https://react.i18next.com/)
做双语（中文 `zh` / 英文 `en`）。中文是权威源，英文对应翻译。

### 架构

| 文件 | 作用 |
|------|------|
| `src/lib/i18n.ts` | i18next 初始化：注册 `zh`/`en` resources，`lng` 从 `localStorage["vigil_lang"]` 读、默认 `zh`，`fallbackLng: "zh"`；导出 `setLanguage()` 与 `SUPPORTED_LANGS`。 |
| `src/locales/zh.ts` | **权威**中文文案（普通对象，值为 string）。导出 `Resources` 结构类型。 |
| `src/locales/en.ts` | 英文翻译，类型标注为 `Resources` —— **key 结构与 zh 不一致会编译报错**（`pnpm build` 拦截），杜绝“英文界面残留中文”。 |

初始化是**副作用导入**：`main.tsx` 在渲染前 `import "@/lib/i18n"`，确保首帧即有语言。

### key 命名约定

按「区域/页面」分节：`common.*`（确认/取消/关闭/分页…）、`nav.*`（导航+登出+语言）、
`enum.severity.*` / `enum.status.*`（严重度/状态，多页复用）、`login.*`、`changePassword.*`、
`dashboard.*`、`incidents.*`、`settings.*`。插值用 `{{var}}`（如 `incidents.summary`、`dashboard.noiseRate`）。

### 用法

```tsx
import { useTranslation } from "react-i18next";
function Foo() {
  const { t } = useTranslation();
  return <h1>{t("dashboard.title")}</h1>;          // 简单文案
  // 带插值：t("incidents.summary", { total })
}
```

### 语言切换

侧边栏（`app-shell.tsx`）底部有语言下拉，调用 `setLanguage("zh" | "en")` ——
`i18next.changeLanguage` + 写 `localStorage`，react-i18next 自动重渲染，刷新后保持。

### 新增文案 / 新语言

- **新文案**：先在 `zh.ts` 加 key，再到 `en.ts` 补同一 key 的翻译（漏了会编译报错）。
- **新语言**：加 `src/locales/<lang>.ts`（`: Resources`），在 `i18n.ts` 的 `resources` 与
  `SUPPORTED_LANGS` 注册即可。

### 覆盖范围（诚实记录）

> 全部业务页面与设置子 tab 已完成 `t()` 外化，**zh + en 双语齐全**（key 一致性由
> `en.ts: Resources` 类型在 `tsc` 构建期强制，缺/多 key 直接编译失败）。

**已完整覆盖（zh + en，切到 English 全英文）：**

- 主布局 `components/layout/app-shell.tsx`（全部导航项、登出、语言切换器）
- `pages/login.tsx`（登录页全文案）
- `pages/change-password.tsx`（改密页全文案，含前端校验错误提示与 toast）
- `pages/dashboard.tsx`（标题、4 个 KPI、卡片标题、空态、团队负载回退名）
- `pages/incidents.tsx` / `pages/incident-detail.tsx`（列表 + 详情：筛选、表头、操作、
  时间线、合并、AI 诊断、相似事件、洞察状态/类型枚举）
- `pages/services.tsx` `pages/integrations.tsx` `pages/integration-wizard.tsx`
  `pages/oncall.tsx` `pages/runbooks.tsx` `pages/postmortems.tsx`
  `pages/credentials.tsx` `pages/maintenance.tsx` `pages/ticket-integrations.tsx`
  `pages/webhook-subscriptions.tsx` `pages/escalation-policies.tsx`
  `pages/users-teams.tsx` `pages/wall.tsx`
  （标题、表头、按钮、表单 label/占位、下拉选项、空态、toast、确认文案、状态/类型枚举）
- `pages/settings/index.tsx`（页头 + 6 个 tab 标签）及全部设置子 tab：
  `settings/rbac-tab` `apikey-tab` `audit-tab` `notification-tab`
  `subscription-tab` `im-tab`
- `lib/badges.tsx`（`SeverityBadge` / `StatusBadge`，严重度与状态标签，多页复用）
- `lib/http.ts`（请求错误兜底提示，走 `errors.*`，i18n 单例 `t()`）
- `lib/format.ts`（`formatDuration` 时长单位随语言，走 `format.*`，i18n 单例 `t()`）
- `components/ui/dialog.tsx`（关闭按钮 aria-label）

**待迁移：**

- 无剩余业务页面/设置 tab。仅个别刻意保留为字面量的技术示例文本（非可翻译 UI 文案），如
  通知模板占位符 `{{.Summary}}`（Go template 语法，与 i18next 插值定界符冲突，故不走
  `t()`）、接入向导样例 payload 的 JSON 数据值；以及各文件内中文注释（不渲染，无需外化）。

> QA 目视复核项：切到 English 后逐一核对各页无残留中文；切回中文后一致；刷新语言保持。
> `runbooks` / `settings/apikey` 用 `<Trans>`（含 `<0>`/`<code>` 内联标记），需额外核对
> 富文本渲染正确。

---

# React + TypeScript + Vite（模板说明）

This template provides a minimal setup to get React working in Vite with HMR and some ESLint rules.

Currently, two official plugins are available:

- [@vitejs/plugin-react](https://github.com/vitejs/vite-plugin-react/blob/main/packages/plugin-react) uses [Oxc](https://oxc.rs)
- [@vitejs/plugin-react-swc](https://github.com/vitejs/vite-plugin-react/blob/main/packages/plugin-react-swc) uses [SWC](https://swc.rs/)

## React Compiler

The React Compiler is not enabled on this template because of its impact on dev & build performances. To add it, see [this documentation](https://react.dev/learn/react-compiler/installation).

## Expanding the ESLint configuration

If you are developing a production application, we recommend updating the configuration to enable type-aware lint rules:

```js
export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      // Other configs...

      // Remove tseslint.configs.recommended and replace with this
      tseslint.configs.recommendedTypeChecked,
      // Alternatively, use this for stricter rules
      tseslint.configs.strictTypeChecked,
      // Optionally, add this for stylistic rules
      tseslint.configs.stylisticTypeChecked,

      // Other configs...
    ],
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.node.json', './tsconfig.app.json'],
        tsconfigRootDir: import.meta.dirname,
      },
      // other options...
    },
  },
])
```

You can also install [eslint-plugin-react-x](https://github.com/Rel1cx/eslint-react/tree/main/packages/plugins/eslint-plugin-react-x) and [eslint-plugin-react-dom](https://github.com/Rel1cx/eslint-react/tree/main/packages/plugins/eslint-plugin-react-dom) for React-specific lint rules:

```js
// eslint.config.js
import reactX from 'eslint-plugin-react-x'
import reactDom from 'eslint-plugin-react-dom'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      // Other configs...
      // Enable lint rules for React
      reactX.configs['recommended-typescript'],
      // Enable lint rules for React DOM
      reactDom.configs.recommended,
    ],
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.node.json', './tsconfig.app.json'],
        tsconfigRootDir: import.meta.dirname,
      },
      // other options...
    },
  },
])
```
