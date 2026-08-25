import {
  AssistantRuntimeProvider,
  useAuiState,
  useComposer,
  useThreadRuntime,
} from "@assistant-ui/react";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { CheckIcon, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { ConnectError } from "@connectrpc/connect";
import { responseProfileClient, sessionClient } from "@/api/client";
import { PendingContextProvider } from "@/components/assistant-ui/composer-context";
import { AcpThread } from "./AcpThread";
import { PermissionComposer } from "./AcpThread";
import { SelectionMenu } from "./SelectionMenu";
import { SessionDock } from "./dock/SessionDock";
import {
  useAcpRuntime,
  type AgentStatus,
  type WorkflowInfo,
} from "./useAcpRuntime";

// AcpSession монтируется из SessionGuard только при найденной сессии — иначе
// AG-UI-рантайм поднял бы соединение в никуда ещё до показа 404.
//
// reloadNonce перезагружает рантайм (и вместе с ним историю треда) при появлении
// фоновых сообщений: фоновый turn (agent wakeup после завершения Workflow/задачи)
// копится в history бэкенда, но живьём в тред не стримится — sink привязан только на
// время /run. Инкремент ремоунтит AcpSessionInner, и history-адаптер перечитывает ленту.
export function AcpSession({
  sessionId,
  workspace = false,
  experience,
}: {
  sessionId: string;
  workspace?: boolean;
  experience?: ReactNode;
}) {
  const [reloadNonce, setReloadNonce] = useState(0);
  // Guard живёт выше remount: иначе каждый status poll с generating=true создаёт
  // новый runtime и обрывает replay-SSE до подключения.
  const reloadPending = useRef(false);
  const reload = useCallback(() => {
    if (reloadPending.current) return;
    reloadPending.current = true;
    setReloadNonce((n) => n + 1);
  }, []);
  const finishReload = useCallback(() => {
    reloadPending.current = false;
  }, []);

  return (
    <AcpSessionInner
      key={reloadNonce}
      sessionId={sessionId}
      workspace={workspace}
      experience={experience}
      onReload={reload}
      onReloadFinished={finishReload}
    />
  );
}

function AcpSessionInner({
  sessionId,
  workspace,
  experience,
  onReload,
  onReloadFinished,
}: {
  sessionId: string;
  workspace: boolean;
  experience?: ReactNode;
  onReload: () => void;
  onReloadFinished: () => void;
}) {
  const [responseProfiles, setResponseProfiles] = useState<{ id: string; name: string; deleted?: boolean }[]>([]);
  const [responseProfileId, setResponseProfileId] = useState("default");
  const [responseProfileBusy, setResponseProfileBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    Promise.all([responseProfileClient.list({}), sessionClient.get({ sessionId })])
      .then(([profiles, session]) => {
        if (cancelled) return;
        const selected = session.session?.responseProfileId || "default";
        const items: { id: string; name: string; deleted?: boolean }[] = profiles.profiles.map((profile) => ({ id: profile.id, name: profile.name }));
        if (!items.some((profile) => profile.id === selected)) {
          items.push({ id: selected, name: session.session?.responseProfileName || "Удалённый профиль", deleted: true });
        }
        setResponseProfiles(items);
        setResponseProfileId(selected);
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [sessionId]);

  const changeResponseProfile = useCallback(async (id: string) => {
    if (id === responseProfileId) return;
    setResponseProfileBusy(true);
    try {
      const result = await sessionClient.setSessionResponseProfile({ sessionId, responseProfileId: id });
      setResponseProfileId(result.session?.responseProfileId || "default");
      toast.success("Профиль ответов применён");
    } catch (error) {
      toast.error(error instanceof ConnectError ? error.rawMessage : "Не удалось применить профиль");
    } finally {
      setResponseProfileBusy(false);
    }
  }, [sessionId, responseProfileId]);

  const {
    runtime,
    permission,
    resolvePermission,
    commands,
    plan,
    a2ui,
    configOptions,
    setConfigOption,
    status,
    refreshStatus,
    workflows,
  } = useAcpRuntime(sessionId);

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <PendingContextProvider>
      <div className="relative flex h-full flex-col">
        <div className="min-h-0 flex-1">
          {experience ? (
            <ExperienceHost
              permission={permission}
              onPermissionDecision={(decision) => permission && resolvePermission(permission.id, decision)}
            >
              {experience}
            </ExperienceHost>
          ) : (
            <AcpThread
              sessionId={sessionId}
              workspace={workspace}
              commands={commands}
              plan={plan}
              a2ui={a2ui}
              configOptions={configOptions}
              permission={permission}
              onPermissionDecision={(decision) =>
                permission && resolvePermission(permission.id, decision)
              }
              onConfigChange={(configId, value) =>
                void setConfigOption(configId, value)
              }
              responseProfiles={responseProfiles}
              responseProfileId={responseProfileId}
              responseProfileBusy={responseProfileBusy}
              onResponseProfileChange={(id) => void changeResponseProfile(id)}
            />
          )}
        </div>
        <BackgroundActivity
          status={status}
          onReload={onReload}
          onReloadFinished={onReloadFinished}
          refreshStatus={refreshStatus}
        />
        <WorkflowsPanel workflows={workflows} />
        {/* Плавающая обвязка сессии: чипы окон, шкала навигации по ленте, терминал.
            Внутри провайдера рантайма — шкале и ссылкам нужна лента сообщений. */}
        {!workspace && <SessionDock sessionId={sessionId} />}
      </div>
      <SelectionMenu />
      </PendingContextProvider>
    </AssistantRuntimeProvider>
  );
}

function ExperienceHost({
  children,
  permission,
  onPermissionDecision,
}: {
  children: ReactNode;
  permission: ReturnType<typeof useAcpRuntime>["permission"];
  onPermissionDecision: (decision: string) => void;
}) {
  return (
    <div className="relative h-full min-h-0 overflow-hidden">
      {children}
      {permission && (
        <div className="absolute inset-x-4 bottom-4 z-30 mx-auto max-w-2xl">
          <PermissionComposer permission={permission} onDecide={onPermissionDecision} />
        </div>
      )}
    </div>
  );
}

// BackgroundActivity подключает обычный runtime к фоновому turn и перезагружает историю,
// когда в ленте бэкенда появились уже завершённые сообщения, не прошедшие через живой
// прогон этого клиента. Детекция завершённых сообщений — по
// росту seq в покое, а не по наблюдению generating: setInterval в фоновой вкладке
// троттлится браузером до 1/мин, и короткий фоновый turn целиком проваливается между
// поллами — момент generating=true можно не увидеть вовсе, но рост seq неустраним.
// Активный turn подключается отдельным replay-run: UI получает обычное последнее сообщение,
// pulsing indicator и Stop вместо специальной плашки. Перезагрузку завершённой истории
// откладываем, пока в поле ввода есть недописанный текст.
function BackgroundActivity({
  status,
  onReload,
  onReloadFinished,
  refreshStatus,
}: {
  status: AgentStatus;
  onReload: () => void;
  onReloadFinished: () => void;
  refreshStatus: () => void;
}) {
  const isRunning = useAuiState((s) => s.thread.isRunning);
  const isLoading = useAuiState((s) => s.thread.isLoading);
  const lastMessageId = useAuiState((s) => s.thread.messages.at(-1)?.id ?? null);
  const composerText = useComposer((c) => c.text);
  const thread = useThreadRuntime();

  // idleSeq — seq ленты, до которого тред синхронизирован. null до первого достоверного
  // полла: маунт уже загрузил полную историю, поэтому первый полл лишь задаёт базу — без
  // этого ремоунт (сбрасывающий status на дефолт {seq:0}) уходил бы в цикл перезагрузок
  // (свежий seq всегда больше нуля).
  const idleSeq = useRef<number | null>(null);
  const wasRunning = useRef(isRunning);
  // cooldownTick — tick на момент завершения foreground-прогона. isRunning гаснет
  // мгновенно (SSE RUN_FINISHED), а кэш status обновляется поллингом — до свежего полла
  // (tick > cooldownTick) и generating, и seq могут быть stale: generating ложно-true
  // (мигание индикатора), seq занижен (события прогона ещё не учтены — сдвиг базы по
  // нему дал бы ложный «фоновый рост» и лишний ремоунт).
  const cooldownTick = useRef<number | null>(null);
  const reconnectTick = useRef<number | null>(null);

  useEffect(() => {
    // До первого реального полла status — синтетический дефолт ({seq:0, tick:0}), а не
    // наблюдение. Принять его за базу нельзя: первый настоящий полл принёс бы seq>0,
    // «рост» породил бы ремоунт, ремоунт — снова дефолт и новую базу 0 — бесконечный
    // цикл перезагрузок. Ждём первый полл (tick >= 1).
    if (status.tick === 0) return;
    if (isRunning) {
      onReloadFinished();
      // Foreground-прогон: сообщения стримятся в тред живьём. База синхронизируется на
      // выходе из остывания; здесь только фиксируем факт прогона.
      cooldownTick.current = null;
      wasRunning.current = true;
      return;
    }
    if (wasRunning.current) {
      // Прогон только что завершился. Входим в остывание и просим немедленный полл:
      // ждать штатного тика нельзя — в затроттленной вкладке он может прийти через
      // минуту, и фоновые события успели бы слиться с событиями прогона в одной дельте.
      wasRunning.current = false;
      cooldownTick.current = status.tick;
      refreshStatus();
      return;
    }
    if (cooldownTick.current !== null) {
      if (status.tick <= cooldownTick.current) {
        return; // stale-полл: ни generating, ни seq не достоверны — ждём свежий.
      }
      // Свежий полл после конца прогона: синхронизируем базу (события прогона уже в
      // треде — отрисованы живым стримом).
      cooldownTick.current = null;
      idleSeq.current = status.seq;
    }

    if (status.generating) {
      // History adapter сам SSE не запускает. Явно открываем replay-run с маркером,
      // чтобы backend привязался к живому turn, но не отправил последнее user-сообщение
      // агенту повторно. Один запрос на status-tick защищает от request storm при ошибке.
      if (!isLoading && reconnectTick.current !== status.tick) {
        reconnectTick.current = status.tick;
        thread.startRun({
          parentId: lastMessageId,
          runConfig: { custom: { brigadeReplay: true } },
        });
      }
      return;
    }
    reconnectTick.current = null;
    onReloadFinished();

    if (idleSeq.current === null) {
      idleSeq.current = status.seq; // первый достоверный полл — задаём базу без reload
      return;
    }
    // Покой: рост seq сверх базы = в ленте появились сообщения, которых тред не видел.
    // Перезагружаем историю (ремоунт рантайма), когда поле ввода пусто — иначе ремоунт
    // затёр бы недописанный текст (база не двигается, повторим по очистке ввода).
    if (status.seq > idleSeq.current && composerText.trim() === "") {
      idleSeq.current = status.seq;
      onReload();
    }
  }, [
    isRunning,
    isLoading,
    lastMessageId,
    status.generating,
    status.seq,
    status.tick,
    composerText,
    onReload,
    onReloadFinished,
    refreshStatus,
    thread,
  ]);

  return null;
}

// WorkflowsPanel — панель фоновых workflow-запусков харнесса (deep-research и т.п.):
// они выполняются между turn'ами, ACP-событий не эмитят, и без панели пользователь не
// видит, что в сессии вообще что-то происходит. Показываются активные запуски (прогресс
// по субагентам) и только что завершившиеся (короткое окно, чтобы увидеть финал).
function WorkflowsPanel({ workflows }: { workflows: WorkflowInfo[] }) {
  const shown = workflows.filter(
    (wf) => wf.active || (wf.done && wf.lastActivitySec < 120),
  );
  if (shown.length === 0) return null;
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-40 z-10 flex flex-col items-center gap-1">
      {shown.map((wf) => (
        <div
          key={wf.runId}
          className="bg-muted/90 text-muted-foreground flex max-w-[90%] items-center gap-2 rounded-full border px-3 py-1.5 text-xs shadow-sm backdrop-blur"
        >
          {wf.done ? (
            <CheckIcon className="size-3.5 shrink-0 text-green-600" />
          ) : (
            <Loader2 className="size-3.5 shrink-0 animate-spin" />
          )}
          <span className="truncate font-medium">{wf.name}</span>
          <span className="shrink-0">
            {wf.done
              ? "завершён"
              : `агентов ${wf.agentsDone}/${wf.agentsStarted}`}
          </span>
          {!wf.done && (
            <span className="shrink-0 opacity-70">
              · {formatAgo(wf.lastActivitySec)}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}

// formatAgo — компактное «сколько назад» для панели: секунды до минуты, дальше минуты.
function formatAgo(sec: number): string {
  if (sec < 60) return `${sec}с назад`;
  return `${Math.floor(sec / 60)}м назад`;
}
