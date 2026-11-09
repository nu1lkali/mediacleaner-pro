import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { AlertCircle, Eye, EyeOff, HardDrive, Loader2, Lock, ShieldCheck, User } from "lucide-react";
import { api, ApiError } from "../services/api";
import { useAuth } from "../lib/auth";
import { Button } from "../components/ui/button";
import type { Bootstrap } from "../services/types";

export default function LoginPage() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const location = useLocation() as { state?: { from?: string } };

  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(true);
  const [showPwd, setShowPwd] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [boot, setBoot] = useState<Bootstrap | null>(null);

  useEffect(() => {
    api
      .bootstrap()
      .then(setBoot)
      .catch(() => setBoot(null));
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setError(null);
    setBusy(true);
    try {
      await login(username.trim(), password, remember);
      const to = location.state?.from;
      navigate(to && to !== "/login" ? to : "/dashboard", { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "登录失败，请稍后重试");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden bg-background px-4 py-10">
      {/* 背景装饰 */}
      <div className="pointer-events-none absolute inset-0">
        <div className="absolute -left-40 -top-40 h-[420px] w-[420px] rounded-full bg-primary/10 blur-3xl" />
        <div className="absolute -bottom-52 -right-32 h-[480px] w-[480px] rounded-full bg-primary/5 blur-3xl" />
      </div>

      <div className="relative grid w-full max-w-[880px] overflow-hidden rounded-2xl border border-border bg-card shadow-lift md:grid-cols-[1.05fr_1fr]">
        {/* 左侧品牌区 */}
        <div className="hidden flex-col justify-between bg-gradient-to-br from-primary/95 to-primary/70 p-8 text-primary-foreground md:flex">
          <div className="flex items-center gap-2.5">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-white/15 backdrop-blur">
              <HardDrive className="h-4 w-4" />
            </div>
            <div>
              <div className="text-sm font-semibold leading-tight">MediaCleaner Pro</div>
              <div className="text-[11px] leading-tight opacity-80">NAS 媒体重复检测与清理</div>
            </div>
          </div>

          <div className="space-y-4">
            <h1 className="text-[22px] font-semibold leading-snug">
              百万文件的分层去重
              <br />
              安全可回滚的批量清理
            </h1>
            <ul className="space-y-2 text-[12.5px] opacity-90">
              {[
                "元数据 → 采样指纹 → 全文校验，逐层过滤不做无用 IO",
                "完全重复与相似媒体严格分开，感知哈希不等于可删",
                "删除默认进隔离区，可恢复、可追溯、有过期清理",
                "跨 volume 统一索引，支持增量扫描与断点续扫",
              ].map((t) => (
                <li key={t} className="flex gap-2">
                  <ShieldCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>{t}</span>
                </li>
              ))}
            </ul>
          </div>

          <div className="text-[11px] opacity-70">
            {boot ? `版本 ${boot.version}` : "正在读取服务信息…"}
          </div>
        </div>

        {/* 右侧表单 */}
        <div className="p-8">
          <div className="mb-6">
            <h2 className="text-lg font-semibold">登录控制台</h2>
            <p className="mt-1 text-[13px] text-muted-foreground">
              请输入账号密码以访问媒体库与清理功能
            </p>
          </div>

          {boot && !boot.initialized && (
            <div className="mb-4 flex gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-[12px] text-warning">
              <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                尚未初始化管理员账号。请查看 <code className="font-mono">config/initial-admin.txt</code>，
                或通过环境变量 <code className="font-mono">MC_ADMIN_PASSWORD</code> 指定初始密码后重启。
              </span>
            </div>
          )}

          <form onSubmit={submit} className="space-y-3.5">
            <div>
              <label className="label" htmlFor="username">
                用户名
              </label>
              <div className="relative">
                <User className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <input
                  id="username"
                  className="input pl-8"
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="admin"
                  required
                />
              </div>
            </div>

            <div>
              <label className="label" htmlFor="password">
                密码
              </label>
              <div className="relative">
                <Lock className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <input
                  id="password"
                  className="input pl-8 pr-9"
                  type={showPwd ? "text" : "password"}
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="••••••••"
                  required
                  autoFocus
                />
                <button
                  type="button"
                  onClick={() => setShowPwd((v) => !v)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground hover:text-foreground"
                  tabIndex={-1}
                >
                  {showPwd ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                </button>
              </div>
            </div>

            <label className="flex cursor-pointer select-none items-center gap-2 text-[12.5px] text-muted-foreground">
              <input
                type="checkbox"
                className="h-3.5 w-3.5 rounded border-input accent-[hsl(var(--primary))]"
                checked={remember}
                onChange={(e) => setRemember(e.target.checked)}
              />
              保持登录状态
            </label>

            {error && (
              <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[12.5px] text-destructive">
                <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>{error}</span>
              </div>
            )}

            <Button type="submit" variant="primary" className="w-full" disabled={busy}>
              {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {busy ? "登录中…" : "登录"}
            </Button>
          </form>

          <p className="mt-5 text-center text-[11px] leading-relaxed text-muted-foreground">
            登录状态使用 HttpOnly Cookie 保存，密码以 PBKDF2-SHA256 加盐存储。
          </p>
        </div>
      </div>
    </div>
  );
}
