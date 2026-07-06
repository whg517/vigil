/**
 * i18n 初始化（i18next + react-i18next）。
 *
 * - 单命名空间（translation），resources 引 zh/en。
 * - 语言来源：localStorage[LANG_KEY] → 默认 zh；fallbackLng: zh（缺 key 回落中文）。
 * - escapeValue: false —— React 已对插值做 XSS 转义，i18next 无需再转义。
 *
 * 在 main.tsx 于渲染前 `import "@/lib/i18n"` 触发一次 init（副作用导入）。
 * 切换语言用 setLanguage()，会同时持久化到 localStorage。
 */
import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import zh from "@/locales/zh";
import en from "@/locales/en";

export const LANG_KEY = "vigil_lang";
export type Lang = "zh" | "en";

/** 支持的语言（用于语言切换器渲染）。 */
export const SUPPORTED_LANGS: { value: Lang; label: string }[] = [
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
];

function initialLang(): Lang {
  const saved = localStorage.getItem(LANG_KEY);
  return saved === "en" || saved === "zh" ? saved : "zh";
}

void i18n.use(initReactI18next).init({
  resources: {
    zh: { translation: zh },
    en: { translation: en },
  },
  lng: initialLang(),
  fallbackLng: "zh",
  interpolation: {
    // React 已做 XSS 转义，关闭 i18next 二次转义（否则中文/符号被 HTML 实体化）。
    escapeValue: false,
  },
});

/** 切换语言并持久化。组件里调用后 react-i18next 会自动重渲染。 */
export function setLanguage(lang: Lang): void {
  localStorage.setItem(LANG_KEY, lang);
  void i18n.changeLanguage(lang);
}

export default i18n;
