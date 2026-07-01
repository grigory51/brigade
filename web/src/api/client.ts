import {
  Code,
  ConnectError,
  createPromiseClient,
  type Interceptor,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthService } from "./gen/brigade/v1/auth_connect";
import { SessionService } from "./gen/brigade/v1/session_connect";
import { AgentService } from "./gen/brigade/v1/agent_connect";

// refreshOnUnauthenticated — Connect-интерсептор тихого обновления access-токена.
// Короткий access-токен (минуты) живёт в httpOnly-cookie; при его истечении вызов
// падает с Unauthenticated. Тогда интерсептор один раз дёргает AuthService.Refresh
// (refresh-токен бэкенд берёт из httpOnly-cookie brigade_refresh, переживающей
// перезагрузку страницы) и повторяет исходный запрос. Это избавляет от разлогина по
// таймауту access-токена. Сам Refresh из цикла исключён, чтобы не зациклиться.
let refreshInFlight: Promise<unknown> | null = null;

const refreshOnUnauthenticated: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (err) {
    const unauthenticated =
      err instanceof ConnectError && err.code === Code.Unauthenticated;
    if (!unauthenticated || req.method.name === "Refresh") {
      throw err;
    }
    // Параллельные вызовы делят один Refresh, чтобы не ротировать токен многократно.
    refreshInFlight ??= refreshClient
      .refresh({})
      .finally(() => (refreshInFlight = null));
    try {
      await refreshInFlight;
    } catch {
      // Refresh не удался (refresh-cookie истёк/отозван) — пробрасываем исходную ошибку,
      // вызывающий уведёт пользователя на /login.
      throw err;
    }
    return await next(req);
  }
};

// Транспорт Connect в JSON-режиме: unary-вызовы идут обычным POST на
// /brigade.v1.<Service>/<Method>. baseUrl пустой — тот же origin, что и SPA
// (фронтенд встроен в бэкенд через go:embed).
//
// credentials: "include" — браузер шлёт httpOnly-cookie с access-токеном,
// которую бэкенд выставляет на Login. Это основной механизм авторизации web;
// Bearer-заголовок не используется, т.к. JS не имеет доступа к httpOnly-cookie.
export const transport = createConnectTransport({
  baseUrl: "/",
  useBinaryFormat: false,
  interceptors: [refreshOnUnauthenticated],
  fetch: (input, init) => fetch(input, { ...init, credentials: "include" }),
});

// refreshTransport — отдельный транспорт без интерсептора обновления: на нём работает
// сам Refresh, чтобы вызов обновления не рекурсировал через тот же интерсептор.
const refreshTransport = createConnectTransport({
  baseUrl: "/",
  useBinaryFormat: false,
  fetch: (input, init) => fetch(input, { ...init, credentials: "include" }),
});
const refreshClient = createPromiseClient(AuthService, refreshTransport);

export const authClient = createPromiseClient(AuthService, transport);
export const sessionClient = createPromiseClient(SessionService, transport);
export const agentClient = createPromiseClient(AgentService, transport);

// refreshSession принудительно обновляет токены через Refresh (refresh-токен берётся из
// httpOnly-cookie). Используется неконнектовыми путями (AG-UI/SSE поверх обычного fetch),
// которым нужно восстановить сессию при 401, не проходя через Connect-интерсептор.
// Параллельные вызовы делят один обмен. Бросает, если обновление не удалось.
export function refreshSession(): Promise<unknown> {
  refreshInFlight ??= refreshClient
    .refresh({})
    .finally(() => (refreshInFlight = null));
  return refreshInFlight;
}
