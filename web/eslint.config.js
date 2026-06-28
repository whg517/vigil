import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  // e2e 为 Node 环境 + Playwright 测试代码，不套用前端 browser/react 规则
  // （含 process/fetch 等全局，类型严格度要求低）。Playwright 自带类型检查。
  globalIgnores(['dist', 'e2e', 'playwright-report', 'test-results']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // 允许常量导出（shadcn 的 cva variants 是常量，与组件共存符合 UI 库惯例）
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
    },
  },
])
