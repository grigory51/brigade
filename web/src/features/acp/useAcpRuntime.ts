import { useEffect, useMemo, useRef, useState } from "react";
import { HttpAgent, type HttpAgentFetchFn } from "@ag-ui/client";
import { fromAgUiMessages, useAgUiRuntime } from "@assistant-ui/react-ag-ui";
import {
  ExportedMessageRepository,
  type ThreadHistoryAdapter,
} from "@assistant-ui/react";
import { useAuth } from "@/features/auth/AuthContext";
import { refreshSession } from "@/api/client";
import { DEMO_FRONTEND_TOOLS } from "./frontendTools";
import type { PlanEntry } from "./PlanPanel";

// Эндпоинты канонического AG-UI на бэкенде (тот же origin, что и SPA).
const RUN_URL = "/api/ag-ui/run";
const PERMISSION_URL = "/api/ag-ui/permission";
const HISTORY_URL = "/api/ag-ui/history";

// HistoryMessage — сообщение истории чата от бэкенда (GET /api/ag-ui/history).
type HistoryMessage = { id: string; role: string; content: string };

// PendingPermission — активный запрос разрешения (human-in-the-loop). Бэкенд шлёт его
// событием CUSTOM {name:"permission_request"}; ответ уходит отдельным POST.
export type PendingPermission = {
  id: string;
  title: string;
  options: PermissionOption[];
};

export type PermissionOption = {
  // optionId возвращается обратно как decision.
  optionId: string;
  name?: string;
  // kind подсказывает визуальный акцент кнопки (ACP: allow_once/reject_once/…).
  kind?: string;
};

// AvailableCommand — slash-команда агента; бэкенд шлёт список CUSTOM
// {name:"available_commands"}. Используется автокомплитом composer'а.
export type AvailableCommand = {
  name: string;
  description: string;
  hint?: string;
};

// AcpRuntime — связка ассистент-рантайма AG-UI и побочных каналов (permission/commands/
// plan), которые идут вне основного потока сообщений: CUSTOM-событиями и STATE_SNAPSHOT.
export type AcpRuntime = {
  runtime: ReturnType<typeof useAgUiRuntime>;
  permission: PendingPermission | null;
  resolvePermission: (id: string, decision: string) => void;
  commands: AvailableCommand[];
  plan: PlanEntry[];
};

// CustomEventValue — нетипизированная полезная нагрузка CUSTOM-события AG-UI.
type CustomEventValue = Record<string, unknown> | undefined;

export function useAcpRuntime(sessionId: string): AcpRuntime {
  const { getAccessToken } = useAuth();
  const [permission, setPermission] = useState<PendingPermission | null>(null);
  const [commands, setCommands] = useState<AvailableCommand[]>([]);
  const [plan, setPlan] = useState<PlanEntry[]>([]);

  // getAccessToken стабилен (useCallback в AuthContext), но фиксируем в ref, чтобы не
  // пересоздавать агента: HttpAgent читает свежий токен на каждый запрос через fetch.
  const tokenRef = useRef(getAccessToken);
  tokenRef.current = getAccessToken;

  // HttpAgent создаётся один раз на сессию. Кастомный fetch добавляет Bearer на каждый
  // запрос (если токен есть в памяти) и credentials: "include": после перезагрузки
  // страницы токена в памяти нет, и аутентификацию несёт httpOnly-cookie brigade_access,
  // которую бэкенд принимает как fallback к Bearer. При 401 (истёк access-токен) один раз
  // обновляем сессию через Refresh (refresh-cookie) и повторяем запрос — иначе долгий
  // SSE-прогон оборвался бы по таймауту access-токена. frontend-tools в
  // RunAgentInput.tools[] кладёт сам рантайм из model-context (через AcpToolUI).
  const agent = useMemo(() => {
    // useBearer=false на повторе: после Refresh актуальный access лежит в cookie, а
    // токен в памяти устарел — повтор идёт по обновлённой cookie.
    const send = (url: Parameters<HttpAgentFetchFn>[0], init: Parameters<HttpAgentFetchFn>[1], useBearer: boolean) => {
      const token = useBearer ? tokenRef.current() : null;
      const headers = new Headers(init.headers);
      if (token) headers.set("Authorization", `Bearer ${token}`);
      return fetch(url, { ...init, headers, credentials: "include" });
    };
    const fetchWithAuth: HttpAgentFetchFn = async (url, init) => {
      const res = await send(url, init, true);
      if (res.status !== 401) return res;
      try {
        await refreshSession();
      } catch {
        return res; // обновить не удалось — отдаём исходный 401
      }
      return send(url, init, false);
    };
    return new HttpAgent({ url: RUN_URL, threadId: sessionId, fetch: fetchWithAuth });
  }, [sessionId]);

  // Подписка на события агента вне основного потока сообщений: permission_request и
  // available_commands идут CUSTOM-событиями, план агента — STATE_SNAPSHOT {plan: [...]}
  // (снимок целиком при каждом изменении, по ACP-контракту).
  useEffect(() => {
    const sub = agent.subscribe({
      onCustomEvent: ({ event }) => {
        if (event.name === "permission_request") {
          setPermission(toPermission(event.value as CustomEventValue));
        } else if (event.name === "available_commands") {
          setCommands(toCommands(event.value as CustomEventValue));
        }
      },
      onStateSnapshotEvent: ({ event }) => {
        setPlan(toPlan(event.snapshot as CustomEventValue));
      },
    });
    return () => sub.unsubscribe();
  }, [agent]);

  // history-адаптер восстанавливает прошлые turn'ы при открытии треда. load() забирает
  // историю чата массивом сообщений (GET /api/ag-ui/history) и отдаёт её рантайму с
  // корректными ролями. Это вместо прежнего SSE-replay: агрегатор @ag-ui/react-ag-ui
  // склеивает все события одного run'а в единственное assistant-сообщение, из-за чего
  // user-реплики из replay терялись (см. бэкенд acp.Client.Messages). append — no-op:
  // сообщения персистятся на бэкенде (история сессии агента), а не на клиенте.
  const history = useMemo<ThreadHistoryAdapter>(
    () => ({
      load: async () => {
        const token = tokenRef.current();
        const res = await fetch(
          `${HISTORY_URL}?threadId=${encodeURIComponent(sessionId)}`,
          { credentials: "include", headers: authHeaders(token) },
        );
        if (!res.ok) return { messages: [] };
        const data = (await res.json()) as {
          messages?: HistoryMessage[];
          commands?: unknown;
        };
        // Команды агента приходят тем же запросом: при открытии треда SSE-прогон не
        // стартует, поэтому CUSTOM-событие available_commands не приходит (см. backend).
        setCommands(toCommands({ commands: data.commands }));
        // Бэкенд отдаёт историю в форме AG-UI-сообщений ({id, role, content}).
        // fromAgUiMessages переводит их в сообщения assistant-ui (с поддержкой
        // tool-call/reasoning, если они появятся), а ExportedMessageRepository.fromArray
        // выстраивает линейную цепочку parentId — это идиоматичный путь восстановления
        // истории для AG-UI-рантайма (см. assistant-ui docs: ag-ui/runtime-options).
        const raw = Array.isArray(data.messages) ? data.messages : [];
        return ExportedMessageRepository.fromArray(
          fromAgUiMessages(raw, { showThinking: true }),
        );
      },
      append: async () => {},
    }),
    [sessionId],
  );

  const runtime = useAgUiRuntime({
    agent,
    showThinking: true,
    adapters: { history },
  });

  // resolvePermission отправляет решение пользователя отдельным POST с Bearer и
  // снимает диалог. Доставка best-effort: бэкенд отвечает 204 в любом случае.
  const resolvePermission = useRef((id: string, decision: string) => {
    void fetch(PERMISSION_URL, {
      method: "POST",
      credentials: "include",
      headers: authHeaders(tokenRef.current()),
      body: JSON.stringify({ threadId: sessionId, id, decision }),
    });
    setPermission((cur) => (cur && cur.id === id ? null : cur));
  }).current;

  return { runtime, permission, resolvePermission, commands, plan };
}

// DEMO_FRONTEND_TOOLS реэкспортируем как контракт инструментов RunAgentInput.tools[]:
// прогон передаёт их агенту, бэкенд транслирует вызовы в TOOL_CALL_*.
export { DEMO_FRONTEND_TOOLS };

function authHeaders(token: string | null): Record<string, string> {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h.Authorization = `Bearer ${token}`;
  return h;
}

// toPermission нормализует value события permission_request в PendingPermission.
// При отсутствии вариантов подставляем разрешить/отклонить, чтобы диалог не завис.
function toPermission(value: CustomEventValue): PendingPermission {
  const id = typeof value?.id === "string" ? value.id : "";
  const title =
    typeof value?.title === "string" ? value.title : "Требуется разрешение";
  const rawOptions = Array.isArray(value?.options) ? value.options : [];
  const options: PermissionOption[] = rawOptions
    .map((o) => o as Record<string, unknown>)
    .filter((o) => typeof o.optionId === "string")
    .map((o) => ({
      optionId: o.optionId as string,
      name: typeof o.name === "string" ? o.name : undefined,
      kind: typeof o.kind === "string" ? o.kind : undefined,
    }));
  return {
    id,
    title,
    options:
      options.length > 0
        ? options
        : [
            { optionId: "allow", name: "Разрешить", kind: "allow_once" },
            { optionId: "reject", name: "Отклонить", kind: "reject_once" },
          ],
  };
}

// toCommands нормализует value события available_commands в список slash-команд.
// toPlan нормализует снимок состояния STATE_SNAPSHOT в план агента. Пустой/чужой
// снимок даёт пустой план (панель скрывается).
function toPlan(snapshot: CustomEventValue): PlanEntry[] {
  const raw = Array.isArray(snapshot?.plan) ? snapshot.plan : [];
  return raw
    .map((e) => e as Record<string, unknown>)
    .filter((e) => typeof e.content === "string")
    .map((e) => ({
      content: e.content as string,
      status: typeof e.status === "string" ? e.status : "pending",
      priority: typeof e.priority === "string" ? e.priority : undefined,
    }));
}

function toCommands(value: CustomEventValue): AvailableCommand[] {
  const raw = Array.isArray(value?.commands) ? value.commands : [];
  return raw
    .map((c) => c as Record<string, unknown>)
    .filter((c) => typeof c.name === "string")
    .map((c) => ({
      name: c.name as string,
      description: typeof c.description === "string" ? c.description : "",
      hint: typeof c.hint === "string" ? c.hint : undefined,
    }));
}
