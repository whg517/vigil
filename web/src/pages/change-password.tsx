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
import { useChangePassword } from "@/hooks/auth";
import { isAuthenticated, logout } from "@/lib/auth";
import { extractError } from "@/lib/http";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Shield } from "lucide-react";

export function ChangePassword() {
  const navigate = useNavigate();
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
      setLocalError("两次输入的新密码不一致");
      return;
    }
    if (newPassword === oldPassword) {
      setLocalError("新密码不能与旧密码相同");
      return;
    }
    if (newPassword.length < 8) {
      setLocalError("新密码至少 8 位");
      return;
    }
    change.mutate(
      { oldPassword, newPassword },
      {
        onSuccess: () => {
          // 改密后旧 token 已被吊销：登出并跳登录页，用新密码重新登录。
          toast.success("密码已修改，请用新密码重新登录");
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
          <h1 className="text-xl font-semibold">修改密码</h1>
          <p className="text-sm text-slate-500">为保障账号安全，请设置新密码</p>
        </div>

        <form className="space-y-3" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">当前密码</label>
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
            <label className="text-sm font-medium text-slate-700">新密码</label>
            <Input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              placeholder="至少 8 位，含字母与数字/符号"
              required
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">确认新密码</label>
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              placeholder="再次输入新密码"
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
            {change.isPending ? "提交中..." : "修改密码"}
          </Button>
        </form>

        <p className="text-center text-xs text-slate-400">
          修改成功后将退出登录，请用新密码重新登录
        </p>
      </div>
    </div>
  );
}
