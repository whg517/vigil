import { useEffect, useState } from "react";
import { Moon } from "lucide-react";
import { useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import {
  NIGHT_PROMPT_DISMISSED_KEY,
  NIGHT_PROMPT_SESSION_KEY,
  isCoreResponsePath,
  isNightHours,
  useTheme,
} from "@/lib/theme";

/**
 * NightDarkModePrompt —— 夜间(22:00–07:00)首次访问核心响应页时的暗色强引导（ADR-0034）。
 *
 * 以「是否核心响应页」作为内层组件的挂载开关：进入核心页时 Gate 重新挂载，
 * 在挂载初始化时一次性判定是否弹窗——避免 effect 里 setState（react-hooks/set-state-in-effect）。
 */
export function NightDarkModePrompt() {
  const { pathname } = useLocation();
  if (!isCoreResponsePath(pathname)) return null;
  return <NightPromptGate />;
}

/**
 * 打扰控制为什么分两级记忆：
 * - sessionStorage：本会话只弹一次——值班人深夜在列表/详情间来回跳转，不能每次导航都被拦；
 * - localStorage「不再提醒」：用户显式勾选后跨会话永久静默——「强引导」不等于每晚骚扰。
 * 偏好已是暗色时无需引导，直接不弹。
 */
function NightPromptGate() {
  const { t } = useTranslation();
  const { theme, setTheme } = useTheme();
  const [dontAsk, setDontAsk] = useState(false);
  // 惰性初始化（纯读取，无副作用）：挂载时一次性判定。
  const [open, setOpen] = useState(
    () =>
      theme !== "dark" &&
      localStorage.getItem(NIGHT_PROMPT_DISMISSED_KEY) !== "1" &&
      sessionStorage.getItem(NIGHT_PROMPT_SESSION_KEY) !== "1" &&
      isNightHours(),
  );

  // 弹出即记会话标记（外部存储副作用，与渲染解耦）：无论用户怎么关掉，本会话内不再重复。
  useEffect(() => {
    if (open) sessionStorage.setItem(NIGHT_PROMPT_SESSION_KEY, "1");
  }, [open]);

  // 关闭时统一处理「不再提醒」：勾选生效与选了哪个按钮无关，语义独立。
  const closeWith = (switchToDark: boolean) => {
    if (dontAsk) localStorage.setItem(NIGHT_PROMPT_DISMISSED_KEY, "1");
    if (switchToDark) setTheme("dark");
    setOpen(false);
  };

  return (
    <Dialog
      open={open}
      onClose={() => setOpen(false)}
      title={t("theme.nightPromptTitle")}
      description={t("theme.nightPromptDesc")}
      className="max-w-md"
    >
      <div className="space-y-4">
        <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={dontAsk}
            onChange={(e) => setDontAsk(e.target.checked)}
            className="h-4 w-4"
          />
          {t("theme.nightPromptDontAsk")}
        </label>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={() => closeWith(false)}>
            {t("theme.nightPromptKeepLight")}
          </Button>
          <Button type="button" onClick={() => closeWith(true)}>
            <Moon className="h-4 w-4" /> {t("theme.nightPromptSwitch")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
