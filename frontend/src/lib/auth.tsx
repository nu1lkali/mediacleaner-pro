import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { api, setUnauthorizedHandler } from "../services/api";
import type { Capabilities, MeResult, User } from "../services/types";

/* ---------------- 认证 ---------------- */

interface AuthCtx {
  user: User | null;
  capabilities: Capabilities | null;
  loading: boolean;
  login: (username: string, password: string, remember: boolean) => Promise<void>;
  logout: () => Promise<void>;
  reload: () => Promise<void>;
  isAdmin: boolean;
}

const AuthContext = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null);
  const [loading, setLoading] = useState(true);

  const apply = useCallback((data: MeResult) => {
    setUser(data.user);
    setCapabilities(data.capabilities);
  }, []);

  const reload = useCallback(async () => {
    try {
      const data = await api.me();
      apply(data);
    } catch {
      setUser(null);
    }
  }, [apply]);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const data = await api.me();
        if (alive) apply(data);
      } catch {
        if (alive) setUser(null);
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [apply]);

  // 任何接口返回 401 时立刻清空登录态，路由层据此跳转登录页
  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null));
    return () => setUnauthorizedHandler(null);
  }, []);

  const login = useCallback(
    async (username: string, password: string, remember: boolean) => {
      const res = await api.login(username, password, remember);
      setUser(res.user);
      setCapabilities(res.capabilities);
    },
    [],
  );

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
    }
  }, []);

  const value = useMemo<AuthCtx>(
    () => ({
      user,
      capabilities,
      loading,
      login,
      logout,
      reload,
      isAdmin: user?.role === "admin",
    }),
    [user, capabilities, loading, login, logout, reload],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthCtx {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth 必须在 AuthProvider 内使用");
  return ctx;
}
