import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ConnectError, Code } from "@connectrpc/connect";
import { authClient, desktopClient } from "@/api/client";
import type { User } from "@/api/gen/brigade/v1/auth_pb";

type AuthState = {
  user: User | null;
  // ready === false до завершения первичной проверки сессии (Me). Пока не готово,
  // роутер не должен принимать решение о редиректе, иначе уже залогиненного
  // пользователя выбросит на /login из-за гонки.
  ready: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  // desktop — локальный однопользовательский запуск (Brigade.app): вход выполнен
  // автоматически, поэтому выхода и смены пользователя в интерфейсе нет.
  desktop: boolean;
  // getAccessToken возвращает access-JWT из памяти (выдан Login) для запросов,
  // требующих заголовок Authorization: Bearer, — это AG-UI-эндпоинт ACP-режима,
  // который не читает httpOnly-cookie. null, если токен в памяти отсутствует
  // (например, после перезагрузки страницы — cookie остаётся, но JS её не видит).
  getAccessToken: () => string | null;
};

const AuthCtx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [ready, setReady] = useState(false);
  const [desktop, setDesktop] = useState(false);
  const activeRemoteRef = useRef<string | null>(null);
  // access-JWT хранится в памяти (не в state): он не влияет на рендер, но нужен
  // для Bearer-заголовка AG-UI-запросов. Login кладёт его сюда, Logout очищает.
  const accessTokenRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const bootstrap = async () => {
      try {
        const state = await desktopClient.listEnvironments({});
        if (!cancelled) {
          setDesktop(true);
          activeRemoteRef.current = state.environments.find((environment) => environment.active && environment.kind === "remote")?.id ?? null;
        }
      } catch {
        // DesktopService отсутствует в web/server-режиме.
      }
      return authClient.me({});
    };
    bootstrap()
      .then((u) => {
        if (!cancelled) setUser(u);
      })
      .catch((err) => {
        // Unauthenticated — ожидаемое состояние (нет валидной cookie); прочие
        // ошибки гасим в null-пользователя, чтобы не блокировать загрузку SPA.
        if (
          !(err instanceof ConnectError) ||
          err.code !== Code.Unauthenticated
        ) {
          // Логируем неожиданное, но не падаем.
          console.warn("auth: проверка сессии не удалась", err);
        }
        if (!cancelled) setUser(null);
      })
      .finally(() => {
        if (!cancelled) setReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback(async (username: string, password: string) => {
    if (activeRemoteRef.current) {
      await desktopClient.loginEnvironment({ id: activeRemoteRef.current, username, password });
      window.location.assign("/sessions");
      return;
    }
    const res = await authClient.login({ username, password });
    // access-токен бэкенд кладёт в httpOnly-cookie (для Connect-вызовов) и в тело
    // ответа: тело используем как Bearer для AG-UI-эндпоинта, не читающего cookie.
    accessTokenRef.current = res.accessToken || null;
    setUser(res.user ?? null);
  }, []);

  const logout = useCallback(async () => {
    try {
      await authClient.logout({});
    } finally {
      accessTokenRef.current = null;
      setUser(null);
    }
  }, []);

  const getAccessToken = useCallback(() => accessTokenRef.current, []);

  // В обычном браузере desktop определяется сервером. В Brigade.app он уже установлен
  // успешным вызовом локального DesktopService и не зависит от выбранного remote env.
  useEffect(() => {
    if (!user || desktop) return;
    let cancelled = false;
    void authClient
      .getServerInfo({})
      .then((info) => !cancelled && setDesktop(info.desktop))
      .catch(() => {
        // Старый сервер без метода — считаем обычным веб-режимом.
      });
    return () => {
      cancelled = true;
    };
  }, [user, desktop]);

  const value = useMemo<AuthState>(
    () => ({ user, ready, login, logout, getAccessToken, desktop }),
    [user, ready, login, logout, getAccessToken, desktop],
  );

  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error("useAuth должен вызываться внутри AuthProvider");
  return ctx;
}
