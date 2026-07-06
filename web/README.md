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

> 本轮落地 **i18n 框架 + 语言切换 + 核心流程完整双语**。其余管理页仍为存量中文，
> 属**待增量迁移**（框架已就绪，逐页替换 `t()` 即可，无需再改基建）。

**已完整覆盖（zh + en，切到 English 全英文）：**

- 主布局 `components/layout/app-shell.tsx`（全部导航项、登出、语言切换器）
- `pages/login.tsx`（登录页全文案）
- `pages/change-password.tsx`（改密页全文案，含前端校验错误提示与 toast）
- `pages/dashboard.tsx`（标题、4 个 KPI、卡片标题、空态、团队负载回退名）
- `pages/incidents.tsx`（标题、状态/严重度筛选、表头、分页、空态、升级次数）
- `pages/settings/index.tsx`（页头 + 6 个 tab 标签）
- `lib/badges.tsx`（`SeverityBadge` / `StatusBadge`，严重度与状态标签，多页复用）
- `components/ui/dialog.tsx`（关闭按钮 aria-label）

**待迁移（本轮保留存量中文，后续增量替换）：**

- 页面：`services` `integrations` `integration-wizard` `oncall` `runbooks` `postmortems`
  `credentials` `maintenance` `ticket-integrations` `webhook-subscriptions`
  `escalation-policies` `users-teams` `wall` `incident-detail`
- 设置子 tab：`settings/rbac-tab` `apikey-tab` `audit-tab` `notification-tab`
  `im-tab` `subscription-tab`
- 其余 `components/ui/*` 内如有零星固定中文（多数为组件注释/由调用方传参，不含渲染文案）

> QA 目视复核项：切到 English 后逐一核对上述“已覆盖”页面无残留中文；切回中文后一致；
> 刷新页面语言保持。

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
