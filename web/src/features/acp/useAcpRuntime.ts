import { useEffect, useMemo, useRef, useState } from "react";
import { HttpAgent, type HttpAgentFetchFn } from "@ag-ui/client";
import { fromAgUiMessages, useAgUiRuntime } from "@assistant-ui/react-ag-ui";
import {
  ExportedMessageRepository,
  type ThreadHistoryAdapter,
} from "@assistant-ui/react";
import { MessageProcessor } from "@a2ui/web_core/v0_9";
import type { ReactComponentImplementation } from "@a2ui/react/v0_9";
import { useAuth } from "@/features/auth/AuthContext";
import { refreshSession, acpClient } from "@/api/client";
import type { AcpConfigOption } from "@/api/gen/brigade/v1/acp_pb";
import { DEMO_FRONTEND_TOOLS } from "./frontendTools";
import type { PlanEntry } from "./PlanPanel";
import { cardsCatalog } from "./a2ui/catalog";

// Потоковый turn ACP идёт по SSE в формате @ag-ui/client — единственная сырая
// HTTP-ручка. Управляющие вызовы (история/статус/workflow/отмена/опции/permission) —
// через acpClient (ConnectRPC, brigade.v1.AcpService).
const RUN_URL = "/api/ag-ui/run";
// WORKFLOWS_POLL_MS — интервал опроса workflow-запусков. Реже статуса: endpoint читает
// файлы харнесса с диска, а состояние воркфлоу меняется медленно (минуты).
const WORKFLOWS_POLL_MS = 5000;
// STATUS_POLL_MS — интервал поллинга состояния сессии. Компромисс: достаточно часто для
// живого индикатора фоновой работы, но без заметной нагрузки (запрос дешёвый, без стрима).
const STATUS_POLL_MS = 2000;

// HistoryMessage — сообщение истории чата от бэкенда (AcpService.GetHistory).
// role="tool_call" — карточка вызова инструмента (toolName/argsText/result): без неё
// tool-карточки исчезали бы из ленты при восстановлении истории.
type HistoryMessage = {
  id: string;
  role: string;
  content: string;
  toolName?: string;
  argsText?: string;
  result?: string;
};

// toAgUiHistoryMessage переводит серверное сообщение истории в форму, понятную
// fromAgUiMessages: tool_call — в assistant-сообщение с tool-call-частью (канонический
// снапшот-формат @assistant-ui/react-ag-ui), остальные роли — как есть. result всегда
// определён (пустая строка): часть без result агрегатор считает ещё выполняющейся и
// рисовал бы вечный спиннер.
function toAgUiHistoryMessage(m: HistoryMessage): unknown {
  if (m.role !== "tool_call") return m;
  let args: unknown = {};
  try {
    args = m.argsText ? JSON.parse(m.argsText) : {};
  } catch {
    // Невалидный/обрезанный JSON аргументов — карточка покажет сырой argsText.
  }
  return {
    id: m.id,
    role: "assistant",
    content: [
      {
        type: "tool-call",
        toolCallId: m.id,
        toolName: m.toolName || "tool",
        args,
        argsText: m.argsText ?? "{}",
        result: m.result ?? "",
      },
    ],
  };
}

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

// ConfigOption — конфигурационная опция ACP-сессии (модель, режим прав, усилие):
// селектор с текущим значением. Бэкенд отдаёт снимок с историей и после смены
// значения (AcpService.SetConfigOption); live-обновления приходят CUSTOM
// {name:"config_options"}.
export type ConfigOption = {
  id: string;
  name: string;
  category?: string;
  currentValue: string;
  options: ConfigOptionValue[];
};

export type ConfigOptionValue = {
  value: string;
  name: string;
  description?: string;
};

// A2uiState — процессор A2UI-поверхностей сессии и счётчик их изменений (version
// растёт при создании/удалении поверхности — потребители ре-рендерятся).
export type A2uiState = {
  processor: MessageProcessor<ReactComponentImplementation>;
  version: number;
};

// AcpRuntime — связка ассистент-рантайма AG-UI и побочных каналов (permission/commands/
// plan/a2ui), которые идут вне основного потока сообщений: CUSTOM-событиями и
// STATE_SNAPSHOT.
export type AcpRuntime = {
  runtime: ReturnType<typeof useAgUiRuntime>;
  permission: PendingPermission | null;
  resolvePermission: (id: string, decision: string) => void;
  commands: AvailableCommand[];
  plan: PlanEntry[];
  a2ui: A2uiState;
  configOptions: ConfigOption[];
  setConfigOption: (configId: string, value: string) => Promise<void>;
  status: AgentStatus;
  refreshStatus: () => void;
  workflows: WorkflowInfo[];
};

// WorkflowInfo — workflow-запуск харнесса агента (AcpService.ListWorkflows): оркестрация
// субагентов, выполняющаяся в фоне между turn'ами и не эмитящая ACP-событий. Панель
// фоновых задач — единственная поверхность её видимости.
export type WorkflowInfo = {
  runId: string;
  name: string;
  agentsStarted: number;
  agentsDone: number;
  done: boolean;
  active: boolean;
  lastActivitySec: number;
};

// AgentStatus — лёгкий снимок состояния сессии (AcpService.GetStatus). generating:
// агент сейчас генерирует (живой Prompt или фоновый turn без активного /run — agent
// wakeup после Workflow/задачи). seq: монотонный счётчик событий ленты — по его росту
// вне активного прогона детектируется появление фонового turn'а. tick: номер поллинга,
// растёт на каждый опрос даже при неизменных generating/seq — heartbeat для логики
// фоновой активности (нужен, чтобы фазовый переход отрабатывал по расписанию поллинга).
export type AgentStatus = { generating: boolean; seq: number; tick: number };

// CustomEventValue — нетипизированная полезная нагрузка CUSTOM-события AG-UI.
type CustomEventValue = Record<string, unknown> | undefined;

export function useAcpRuntime(sessionId: string): AcpRuntime {
  const { getAccessToken } = useAuth();
  const [permission, setPermission] = useState<PendingPermission | null>(null);
  const [commands, setCommands] = useState<AvailableCommand[]>([]);
  const [plan, setPlan] = useState<PlanEntry[]>([]);
  const [configOptions, setConfigOptions] = useState<ConfigOption[]>([]);
  const [status, setStatus] = useState<AgentStatus>({
    generating: false,
    seq: 0,
    tick: 0,
  });
  const [workflows, setWorkflows] = useState<WorkflowInfo[]>([]);
  const [a2uiVersion, setA2uiVersion] = useState(0);

  // Процессор A2UI-поверхностей живёт вместе с сессией: бэкенд шлёт поставки
  // server→client сообщений CUSTOM-событием a2ui, процессор интерпретирует их и держит
  // модели поверхностей (surfaceId = toolCallId карточки).
  const a2uiProcessor = useMemo(
    () => new MessageProcessor<ReactComponentImplementation>([cardsCatalog]),
    // Новая сессия — новый процессор с чистыми поверхностями.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sessionId],
  );

  useEffect(() => {
    const bump = () => setA2uiVersion((v) => v + 1);
    const created = a2uiProcessor.onSurfaceCreated(bump);
    const deleted = a2uiProcessor.onSurfaceDeleted(bump);
    return () => {
      created.unsubscribe();
      deleted.unsubscribe();
    };
  }, [a2uiProcessor]);

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
        } else if (event.name === "a2ui") {
          const messages = (event.value as CustomEventValue)?.messages;
          if (Array.isArray(messages)) {
            a2uiProcessor.processMessages(messages);
          }
        } else if (event.name === "config_options") {
          // Снимок опций сессии изменился на стороне агента (value — массив опций).
          setConfigOptions(toConfigOptions(event.value));
        }
      },
      onStateSnapshotEvent: ({ event }) => {
        setPlan(toPlan(event.snapshot as CustomEventValue));
      },
    });
    return () => sub.unsubscribe();
  }, [agent, a2uiProcessor]);

  // Поллинг состояния сессии: пока тред открыт, дёргаем GET /status. Он несёт признак
  // «агент генерирует» (в т.ч. фоновый turn без активного /run) и seq ленты — по ним
  // AcpSession зажигает индикатор фоновой работы и перезагружает историю при появлении
  // фоновых сообщений. Ошибки/401 проглатываем: это фоновый опрос, не критичный к сбою.
  //
  // refreshStatusRef — внеплановый немедленный полл. Нужен в двух местах: (1) на границе
  // «прогон завершился» — setInterval в фоновой вкладке троттлится браузером до 1/мин,
  // и без немедленного полла база seq синхронизировалась бы с опозданием, съедая фоновые
  // события; (2) на visibilitychange — пользователь вернулся во вкладку, догоняем
  // состояние сразу, не дожидаясь ближайшего тика.
  const refreshStatusRef = useRef<() => void>(() => {});
  // Стабильная обёртка: идентичность не меняется между рендерами, чтобы эффекты
  // потребителей (BackgroundActivity) не передёргивались.
  const refreshStatusStable = useRef(() => refreshStatusRef.current()).current;
  useEffect(() => {
    let stopped = false;
    const tick = async () => {
      try {
        const data = await acpClient.getStatus({ threadId: sessionId });
        if (stopped) return;
        setStatus((prev) => ({
          generating: data.generating,
          seq: Number(data.seq),
          tick: prev.tick + 1,
        }));
      } catch {
        // Транзиентный сбой сети — следующий тик повторит.
      }
    };
    refreshStatusRef.current = () => void tick();
    const onVisible = () => {
      if (document.visibilityState === "visible") void tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    void tick();
    const id = window.setInterval(tick, STATUS_POLL_MS);
    return () => {
      stopped = true;
      window.clearInterval(id);
      document.removeEventListener("visibilitychange", onVisible);
      refreshStatusRef.current = () => {};
    };
  }, [sessionId]);

  // Поллинг workflow-запусков харнесса (панель фоновых задач). Отдельно от /status:
  // другой темп и источник (файлы харнесса). Ошибки проглатываем — фоновый опрос.
  useEffect(() => {
    let stopped = false;
    const tick = async () => {
      try {
        const data = await acpClient.listWorkflows({ threadId: sessionId });
        if (stopped) return;
        setWorkflows(
          data.workflows.map((wf) => ({
            runId: wf.runId,
            name: wf.name,
            agentsStarted: wf.agentsStarted,
            agentsDone: wf.agentsDone,
            done: wf.done,
            active: wf.active,
            lastActivitySec: Number(wf.lastActivitySec),
          })),
        );
      } catch {
        // Транзиентный сбой — следующий тик повторит.
      }
    };
    void tick();
    const id = window.setInterval(tick, WORKFLOWS_POLL_MS);
    return () => {
      stopped = true;
      window.clearInterval(id);
    };
  }, [sessionId]);

  // history-адаптер восстанавливает прошлые turn'ы при открытии треда. load() забирает
  // историю чата массивом сообщений (AcpService.GetHistory) и отдаёт её рантайму с
  // корректными ролями. Это вместо прежнего SSE-replay: агрегатор @ag-ui/react-ag-ui
  // склеивает все события одного run'а в единственное assistant-сообщение, из-за чего
  // user-реплики из replay терялись (см. бэкенд acp.Client.Messages). append — no-op:
  // сообщения персистятся на бэкенде (история сессии агента), а не на клиенте.
  const history = useMemo<ThreadHistoryAdapter>(
    () => ({
      load: async () => {
        let data;
        try {
          // 401 (истёкший access-токен) обрабатывает интерсептор acpClient: тихо
          // обновляет сессию и повторяет вызов. Прочие ошибки — пустая история.
          data = await acpClient.getHistory({ threadId: sessionId });
        } catch {
          return { messages: [] };
        }
        // Команды агента и конфигурационные опции приходят тем же вызовом: при открытии
        // треда SSE-прогон не стартует, поэтому CUSTOM-события не приходят.
        setCommands(
          data.commands.map((c) => ({
            name: c.name,
            description: c.description,
            hint: c.hint || undefined,
          })),
        );
        setConfigOptions(configOptionsFromProto(data.configOptions));
        // Сообщения приводим к AG-UI-снапшоту (tool_call → assistant с tool-call-частью),
        // fromAgUiMessages переводит в формат assistant-ui, ExportedMessageRepository
        // выстраивает линейную цепочку parentId (см. assistant-ui: ag-ui/runtime-options).
        const raw: HistoryMessage[] = data.messages.map((m) => ({
          id: m.id,
          role: m.role,
          content: m.content,
          toolName: m.toolName || undefined,
          argsText: m.argsText || undefined,
          result: m.result,
        }));
        return ExportedMessageRepository.fromArray(
          fromAgUiMessages(raw.map(toAgUiHistoryMessage), {
            showThinking: true,
          }),
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
    // onCancel вызывается при клике Stop. Клиентская отмена рантайма гасит только UI и
    // абортит СВОЙ AbortController, но не HTTP-запрос /run (@ag-ui/client абортит fetch
    // отдельным контроллером, который никто не дёргает), поэтому turn агента продолжал
    // генерироваться. Явно шлём session/cancel через отдельный эндпоинт — агент сворачивает
    // turn кооперативно (stopReason=cancelled). Намеренно НЕ зовём agent.abortRun(): не
    // обрываем HTTP/ctx, чтобы весь хвост turn'а пришёл под серверным turn-барьером и не
    // слипся со следующим прогоном (см. backend acp.Client.Cancel/Prompt). Best-effort.
    onCancel: () => {
      void acpClient.cancel({ threadId: sessionId }).catch(() => {});
    },
  });

  // resolvePermission отправляет решение пользователя и снимает диалог. Доставка
  // best-effort: бэкенд отвечает пустым в любом случае.
  const resolvePermission = useRef((id: string, decision: string) => {
    void acpClient
      .resolvePermission({ threadId: sessionId, id, decision })
      .catch(() => {});
    setPermission((cur) => (cur && cur.id === id ? null : cur));
  }).current;

  // setConfigOption меняет значение опции сессии (модель, режим, усилие) и обновляет
  // локальный снимок из ответа бэкенда.
  const setConfigOptionRef = useRef(async (configId: string, value: string) => {
    try {
      const data = await acpClient.setConfigOption({
        threadId: sessionId,
        configId,
        value,
      });
      setConfigOptions(configOptionsFromProto(data.configOptions));
    } catch {
      // Ошибка смены опции — оставляем прежний снимок.
    }
  });

  return {
    runtime,
    permission,
    resolvePermission,
    commands,
    plan,
    a2ui: { processor: a2uiProcessor, version: a2uiVersion },
    configOptions,
    setConfigOption: setConfigOptionRef.current,
    status,
    refreshStatus: refreshStatusStable,
    workflows,
  };
}

// DEMO_FRONTEND_TOOLS реэкспортируем как контракт инструментов RunAgentInput.tools[]:
// прогон передаёт их агенту, бэкенд транслирует вызовы в TOOL_CALL_*.
export { DEMO_FRONTEND_TOOLS };

// configOptionsFromProto переводит опции из типизированного ответа AcpService (уже
// нормализованы бэкендом из union-формата ACP) в ConfigOption UI-слоя, применяя политику
// скрытия небезопасных значений (bypassPermissions). Пустые category/description proto3
// (строка по умолчанию) приводятся к undefined.
function configOptionsFromProto(opts: AcpConfigOption[]): ConfigOption[] {
  return opts.map((o) => {
    const category = o.category || undefined;
    const hidden = category ? HIDDEN_CONFIG_VALUES[category] : undefined;
    return {
      id: o.id,
      name: o.name || o.id,
      category,
      currentValue: o.currentValue,
      options: o.options
        .filter((v) => !hidden?.has(v.value))
        .map((v) => ({
          value: v.value,
          name: v.name || v.value,
          description: v.description || undefined,
        })),
    };
  });
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

// HIDDEN_CONFIG_VALUES — значения опций, скрытые из UI по соображениям безопасности.
// bypassPermissions полностью отключает проверку прав (агент выполняет любые
// инструменты без подтверждения) — слишком опасно для веб-клиента, вырезаем.
const HIDDEN_CONFIG_VALUES: Record<string, ReadonlySet<string>> = {
  mode: new Set(["bypassPermissions"]),
};

// toConfigOptions нормализует снимок опций сессии (массив ACP SessionConfigOption).
// Boolean-опции (unstable) пропускаются: UI показывает только селекторы.
function toConfigOptions(value: unknown): ConfigOption[] {
  const raw = Array.isArray(value) ? value : [];
  return raw
    .map((o) => o as Record<string, unknown>)
    .filter(
      (o) =>
        typeof o.id === "string" &&
        typeof o.currentValue === "string" &&
        Array.isArray(o.options),
    )
    .map((o) => {
      const category = typeof o.category === "string" ? o.category : undefined;
      const hidden = category ? HIDDEN_CONFIG_VALUES[category] : undefined;
      return {
      id: o.id as string,
      name: typeof o.name === "string" ? o.name : (o.id as string),
      category,
      currentValue: o.currentValue as string,
      options: (o.options as unknown[])
        .map((v) => v as Record<string, unknown>)
        .filter((v) => typeof v.value === "string")
        .filter((v) => !hidden?.has(v.value as string))
        .map((v) => ({
          value: v.value as string,
          name: typeof v.name === "string" ? v.name : (v.value as string),
          description:
            typeof v.description === "string" ? v.description : undefined,
        })),
      };
    });
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
