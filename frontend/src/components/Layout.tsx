import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  BarChart3,
  ChevronDown,
  Copy,
  Eraser,
  HardDrive,
  History,
  Images,
  LayoutDashboard,
  LogOut,
  Menu,
  Moon,
  ScanLine,
  Settings,
  ShieldCheck,
  Sparkles,
  Sun,
  Trash2,
  Video,
  X,
} from "lucide-react";
import { api } from "../services/api";
import { useAuth } from "../lib/auth";
import { useTheme } from "../lib/theme";
import { cn, formatBytes, formatNumber, statusText } from "../lib/utils";

const NAV = [
  { to: "/dashboard", label: "总览", icon: LayoutDashboard },
  { to: "/scan", label: "媒体扫描", icon: ScanLine },
  { to: "/duplicates", label: "完全重复", icon: Copy },
  { to: "/similar-video", label: "相似视频", icon: Video },
  { to: "/similar-image", label: "相似图片", icon: Images },
  { to: "/rules", label: "清理规则", icon: Sparkles },
  { to: "/clean", label: "清理任务", icon: Eraser },
  { to: "/history", label: "扫描历史", icon: History },
  { to: "/quarantine", label: "隔离区", icon: Trash2 },
  { to: "/stats", label: "数据统计", icon: BarChart3 },
  { to: "/settings", label: "系统设置", icon: Settings },
];

function titleForPath(path: string) {
  const hit = NAV.find((n) => path === n.to || path.startsWith(`${n.to}/`));
  return hit?.label ?? "MediaCleaner";
}

export default function Layout() {
  const { user, logout } = useAuth();
  const { resolved, toggle } = useTheme();
  const navigate = useNavigate();
  const location = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  // 移动端抽屉：< lg 时侧边栏隐藏，靠顶栏的菜单按钮唤出
  const [drawerOpen, setDrawerOpen] = useState(false);

  // 全局扫描指示器：每 5 秒轮询一次当前任务
  const { data: current } = useQuery({
    queryKey: ["current-scan"],
    queryFn: api.currentScan,
    refetchInterval: 5000,
  });
  const task = current?.task ?? null;

  useEffect(() => {
    if (!menuOpen) return;
    const close = () => setMenuOpen(false);
    window.addEventListener("click", close);
    return () => window.removeEventListener("click", close);
  }, [menuOpen]);

  // 换页就收起抽屉/菜单，避免移动端跳转后遮着内容
  useEffect(() => {
    setDrawerOpen(false);
    setMenuOpen(false);
  }, [location.pathname]);

  // 抽屉打开时支持 Esc 关闭
  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setDrawerOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawerOpen]);

  return (
    <div className="flex h-full overflow-hidden bg-background">
      {/* 移动端抽屉遮罩 */}
      <div
        className={cn(
          "fixed inset-0 z-40 bg-black/50 transition-opacity duration-200 lg:hidden",
          drawerOpen ? "opacity-100" : "pointer-events-none opacity-0",
        )}
        onClick={() => setDrawerOpen(false)}
        aria-hidden="true"
      />

      {/* 侧边栏：桌面常驻，移动端抽屉 */}
      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex w-[264px] shrink-0 flex-col border-r border-border bg-sidebar",
          "transition-transform duration-200 ease-out lg:static lg:z-auto lg:w-[228px] lg:translate-x-0 lg:shadow-none",
          drawerOpen ? "translate-x-0 shadow-lift" : "-translate-x-full",
        )}
      >
        <div className="flex h-14 items-center gap-2.5 border-b border-border px-4">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
            <HardDrive className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold leading-tight">MediaCleaner</div>
            <div className="text-[11px] leading-tight text-muted-foreground">NAS 媒体清理</div>
          </div>
          <button
            onClick={() => setDrawerOpen(false)}
            className="-mr-1 flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted lg:hidden"
            aria-label="关闭菜单"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <nav className="flex-1 space-y-0.5 overflow-y-auto p-2">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              onClick={() => setDrawerOpen(false)}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2.5 rounded-md px-2.5 py-2.5 text-[13px] transition-colors lg:py-2",
                  isActive
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-sidebar-foreground hover:bg-muted",
                )
              }
            >
              <item.icon className="h-4 w-4 shrink-0" />
              <span className="truncate">{item.label}</span>
            </NavLink>
          ))}
        </nav>

        {/* 扫描状态 */}
        <div className="border-t border-border p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
          {task ? (
            <button
              onClick={() => navigate("/scan")}
              className="w-full rounded-md border border-border bg-card px-2.5 py-2 text-left hover:border-primary/40"
            >
              <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                <span className="truncate">{task.phase_label || statusText[task.status]}</span>
                <span className="tabular-nums">{formatNumber(task.files_seen)}</span>
              </div>
              <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full bg-primary transition-all"
                  style={{ width: `${Math.min(100, percentOf(task))}%` }}
                />
              </div>
              <div className="mt-1 truncate text-[10px] text-muted-foreground">
                {formatBytes(task.bytes_scanned)} · {statusText[task.status]}
              </div>
            </button>
          ) : (
            <div className="rounded-md px-2.5 py-2 text-[11px] text-muted-foreground">当前无扫描任务</div>
          )}
        </div>
      </aside>

      {/* 主区域 */}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-card px-3 sm:px-5">
          <button
            onClick={() => setDrawerOpen(true)}
            className="-ml-1 flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-foreground hover:bg-muted lg:hidden"
            aria-label="打开菜单"
          >
            <Menu className="h-5 w-5" />
          </button>

          {/* 移动端显示当前页标题 */}
          <div className="min-w-0 flex-1 truncate text-[15px] font-semibold lg:hidden">
            {titleForPath(location.pathname)}
          </div>

          <div className="hidden items-center gap-2 text-sm text-muted-foreground lg:flex">
            <ShieldCheck className="h-4 w-4 text-success" />
            <span>只读扫描 · 删除默认进入隔离区</span>
          </div>

          <div className="ml-auto flex shrink-0 items-center gap-1.5">
            <button
              onClick={toggle}
              title={resolved === "dark" ? "切换到浅色" : "切换到深色"}
              className="flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground sm:h-8 sm:w-8"
            >
              {resolved === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </button>

            <div className="relative">
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  setMenuOpen((v) => !v);
                }}
                className="flex h-9 items-center gap-1.5 rounded-md px-1.5 text-[13px] hover:bg-muted sm:h-8 sm:px-2"
              >
                <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/15 text-[10px] font-semibold text-primary">
                  {user?.username?.slice(0, 1).toUpperCase() ?? "?"}
                </span>
                <span className="hidden max-w-[120px] truncate sm:inline">{user?.username}</span>
                <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
              </button>
              {menuOpen && (
                <div
                  className="absolute right-0 top-10 z-30 w-52 rounded-lg border border-border bg-card p-1 shadow-lift"
                  onClick={(e) => e.stopPropagation()}
                >
                  <div className="px-2.5 py-2 text-[11px] text-muted-foreground">
                    {user?.username} · {user?.role === "admin" ? "管理员" : "只读账号"}
                  </div>
                  <button
                    onClick={() => {
                      setMenuOpen(false);
                      navigate("/settings");
                    }}
                    className="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-[13px] hover:bg-muted sm:py-1.5"
                  >
                    <Settings className="h-3.5 w-3.5" /> 账号与设置
                  </button>
                  <button
                    onClick={async () => {
                      setMenuOpen(false);
                      await logout();
                      navigate("/login", { replace: true });
                    }}
                    className="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-[13px] text-destructive hover:bg-destructive/10 sm:py-1.5"
                  >
                    <LogOut className="h-3.5 w-3.5" /> 退出登录
                  </button>
                </div>
              )}
            </div>
          </div>
        </header>

        {/* 移动端：扫描进行中在顶栏下挂一条极细进度 */}
        {task ? (
          <div className="h-0.5 w-full shrink-0 bg-muted lg:hidden">
            <div
              className="h-full bg-primary transition-all"
              style={{ width: `${Math.min(100, percentOf(task))}%` }}
            />
          </div>
        ) : null}

        <main className="min-h-0 flex-1 overflow-y-auto overscroll-y-contain">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function percentOf(task: { files_seen: number; progress?: Record<string, unknown> }) {
  const p = task.progress?.percent;
  if (typeof p === "number" && p > 0) return p;
  return 0;
}
