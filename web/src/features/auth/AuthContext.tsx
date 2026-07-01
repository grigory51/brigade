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
import { authClient } from "@/api/client";
import type { User } from "@/api/gen/brigade/v1/auth_pb";

type AuthState = {
  user: User | null;
  // ready === false до завершения первичной проверки сессии (Me). Пока не готово,
  // роутер не должен принимать решение о редиректе, иначе уже залогиненного
  // пользователя выбросит на /login из-за гонки.
  ready: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
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
  // access-JWT хранится в памяти (не в state): он не влияет на рендер, но нужен
  // для Bearer-заголовка AG-UI-запросов. Login кладёт его сюда, Logout очищает.
  const accessTokenRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    authClient
      .me({})
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

  const value = useMemo<AuthState>(
    () => ({ user, ready, login, logout, getAccessToken }),
    [user, ready, login, logout, getAccessToken],
  );

  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error("useAuth должен вызываться внутри AuthProvider");
  return ctx;
}
