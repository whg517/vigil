/**
 * 登录页（能力域 13 §登录态）。
 * 用户名密码 → JWT → 存 localStorage → 跳 dashboard。
 * 已登录时访问 /login 自动跳走（避免重复登录）。
 */
import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useLogin } from "@/hooks/auth";
import { isAuthenticated } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Shield } from "lucide-react";

export function Login() {
  const navigate = useNavigate();
  const login = useLogin();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  // 已登录直接跳走（用 Navigate 组件，不在渲染期调 navigate 副作用）
  if (isAuthenticated()) {
    return <Navigate to="/" replace />;
  }

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    login.mutate(
      { username, password },
      { onSuccess: () => navigate("/", { replace: true }) },
    );
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 p-4">
      <div className="w-full max-w-sm space-y-6 rounded-xl border bg-white p-8 shadow-sm">
        <div className="space-y-2 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-lg bg-slate-900 text-white">
            <Shield className="h-6 w-6" />
          </div>
          <h1 className="text-xl font-semibold">Vigil 登录</h1>
          <p className="text-sm text-slate-500">告警处置平台 · 守夜人</p>
        </div>

        <form className="space-y-3" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">用户名</label>
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="admin"
              autoFocus
              required
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-slate-700">密码</label>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••"
              required
            />
          </div>
          <Button type="submit" className="w-full" disabled={login.isPending || !username || !password}>
            {login.isPending ? "登录中..." : "登录"}
          </Button>
        </form>

        <p className="text-center text-xs text-slate-400">
          默认管理员 admin / changeme，首次登录后请立即改密
        </p>
      </div>
    </div>
  );
}
