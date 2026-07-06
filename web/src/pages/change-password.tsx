/**
 * 改密页（能力域 13 §登录态 / T0.4 改密令牌吊销闭环）。
 *
 * 两个入口：
 *   1. 首登强制改密（must_change_password=true）：登录后被守卫重定向到此，改密前不可访问业务页。
 *   2. 主动改密：用户从设置等入口进入。
 *
 * 安全语义（T0.4）：改密成功后后端会自增 token_version 吊销当前 access/refresh，
 * 故本页改密成功后主动登出并跳登录页，让用户用新密码重新登录换发新 token——
 * 避免持已失效的旧 token 继续操作触发 401。
 */
import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { useChangePassword } from "@/hooks/auth";
import { isAuthenticated, logout } from "@/lib/auth";
import { extractError } from "@/lib/http";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Shield } from "lucide-react";

export function ChangePassword() {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const change = useChangePassword();
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [localError, setLocalError] = useState("");

  // 未登录不允许改密（需已鉴权身份）；跳登录页。
  if (!isAuthenticated()) {
    return <Navigate to="/login" replace />;
  }

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setLocalError("");
    // 前端先做一致性与基础强度校验（强度最终以后端为准）。
    if (newPassword !== confirm) {
      setLocalError(t("changePassword.errMismatch"));
      return;
    }
    if (newPassword === oldPassword) {
      setLocalError(t("changePassword.errSameAsOld"));
      return;
    }
    if (newPassword.length < 8) {
      setLocalError(t("changePassword.errTooShort"));
      return;
    }
    change.mutate(
      { oldPassword, newPassword },
      {
        onSuccess: () => {
          // 改密后旧 token 已被吊销：登出并跳登录页，用新密码重新登录。
          toast.success(t("changePassword.successToast"));
          logout();
          navigate("/login", { replace: true });
        },
        onError: (err) => {
          // 后端返回的强度/旧密码错误由 http 拦截器 toast，这里额外行内提示。
          setLocalError(extractError(err));
        },
      },
    );
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 p-4">
      <div className="w-full max-w-sm space-y-6 rounded-xl border bg-white p-8 shadow-sm">
        <div className="space-y-2 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-lg bg-slate-900 text-white">
            <Shield className="h-6 w-6" />
          </div>
          <h1 className="text-xl font-semibold">{t("changePassword.title")}</h1>
          <p className="text-sm text-slate-500">{t("changePassword.subtitle")}</p>
        </div>

        <form className="space-y-3" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">{t("changePassword.current")}</label>
            <Input
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              placeholder="••••••"
              autoFocus
              required
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">{t("changePassword.new")}</label>
            <Input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              placeholder={t("changePassword.newPlaceholder")}
              required
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">{t("changePassword.confirm")}</label>
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              placeholder={t("changePassword.confirmPlaceholder")}
              required
            />
          </div>
          {localError && (
            <p className="text-sm text-destructive">{localError}</p>
          )}
          <Button
            type="submit"
            className="w-full"
            disabled={change.isPending || !oldPassword || !newPassword || !confirm}
          >
            {change.isPending ? t("changePassword.submitting") : t("changePassword.submit")}
          </Button>
        </form>

        <p className="text-center text-xs text-slate-400">
          {t("changePassword.hint")}
        </p>
      </div>
    </div>
  );
}
