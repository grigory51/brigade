import {
  AssistantRuntimeProvider,
  useAuiState,
  useComposer,
} from "@assistant-ui/react";
import { useEffect, useRef, useState } from "react";
import { CheckIcon, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { AcpThread } from "./AcpThread";
import { AcpToolUI } from "./AcpToolUI";
import {
  useAcpRuntime,
  type AgentStatus,
  type PendingPermission,
  type WorkflowInfo,
} from "./useAcpRuntime";

// AcpSession монтируется из SessionGuard только при найденной сессии — иначе
// AG-UI-рантайм поднял бы соединение в никуда ещё до показа 404.
//
// reloadNonce перезагружает рантайм (и вместе с ним историю треда) при появлении
// фоновых сообщений: фоновый turn (agent wakeup после завершения Workflow/задачи)
// копится в history бэкенда, но живьём в тред не стримится — sink привязан только на
// время /run. Инкремент ремоунтит AcpSessionInner, и history-адаптер перечитывает ленту.
export function AcpSession({ sessionId }: { sessionId: string }) {
  const [reloadNonce, setReloadNonce] = useState(0);
  return (
    <AcpSessionInner
      key={reloadNonce}
      sessionId={sessionId}
      onReload={() => setReloadNonce((n) => n + 1)}
    />
  );
}

function AcpSessionInner({
  sessionId,
  onReload,
}: {
  sessionId: string;
  onReload: () => void;
}) {
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
      {/* Регистрация frontend-tool show_choice в model-context рантайма; уходит в
          RunAgentInput.tools[] при следующем прогоне. */}
      <AcpToolUI />

      <div className="relative flex h-full flex-col">
        <div className="min-h-0 flex-1">
          <AcpThread
            commands={commands}
            plan={plan}
            a2ui={a2ui}
            configOptions={configOptions}
            onConfigChange={(configId, value) =>
              void setConfigOption(configId, value)
            }
          />
        </div>
        <BackgroundActivity
          status={status}
          onReload={onReload}
          refreshStatus={refreshStatus}
        />
        <WorkflowsPanel workflows={workflows} />
      </div>

      <PermissionDialog
        permission={permission}
        onDecide={(decision) =>
          permission && resolvePermission(permission.id, decision)
        }
      />
    </AssistantRuntimeProvider>
  );
}

// BackgroundActivity показывает индикатор фоновой работы агента и перезагружает историю,
// когда в ленте бэкенда появились сообщения, не прошедшие через живой прогон этого
// клиента (фоновый turn — agent wakeup после завершения Workflow/задачи). Детекция — по
// росту seq в покое, а не по наблюдению generating: setInterval в фоновой вкладке
// троттлится браузером до 1/мин, и короткий фоновый turn целиком проваливается между
// поллами — момент generating=true можно не увидеть вовсе, но рост seq неустраним.
// generating используется только для индикатора. Перезагрузку откладываем, пока это
// небезопасно (идёт прогон или в поле ввода есть недописанный текст).
function BackgroundActivity({
  status,
  onReload,
  refreshStatus,
}: {
  status: AgentStatus;
  onReload: () => void;
  refreshStatus: () => void;
}) {
  const isRunning = useAuiState((s) => s.thread.isRunning);
  const composerText = useComposer((c) => c.text);

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
  const [bgActive, setBgActive] = useState(false);

  useEffect(() => {
    // До первого реального полла status — синтетический дефолт ({seq:0, tick:0}), а не
    // наблюдение. Принять его за базу нельзя: первый настоящий полл принёс бы seq>0,
    // «рост» породил бы ремоунт, ремоунт — снова дефолт и новую базу 0 — бесконечный
    // цикл перезагрузок. Ждём первый полл (tick >= 1).
    if (status.tick === 0) return;
    if (isRunning) {
      // Foreground-прогон: сообщения стримятся в тред живьём. База синхронизируется на
      // выходе из остывания; здесь только фиксируем факт прогона.
      cooldownTick.current = null;
      wasRunning.current = true;
      setBgActive(false);
      return;
    }
    if (wasRunning.current) {
      // Прогон только что завершился. Входим в остывание и просим немедленный полл:
      // ждать штатного тика нельзя — в затроттленной вкладке он может прийти через
      // минуту, и фоновые события успели бы слиться с событиями прогона в одной дельте.
      wasRunning.current = false;
      cooldownTick.current = status.tick;
      setBgActive(false);
      refreshStatus();
      return;
    }
    if (cooldownTick.current !== null) {
      if (status.tick <= cooldownTick.current) {
        setBgActive(false);
        return; // stale-полл: ни generating, ни seq не достоверны — ждём свежий.
      }
      // Свежий полл после конца прогона: синхронизируем базу (события прогона уже в
      // треде — отрисованы живым стримом).
      cooldownTick.current = null;
      idleSeq.current = status.seq;
    }

    setBgActive(status.generating);
    if (status.generating) return; // фоновый turn идёт: базу держим дофоновой.

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
    status.generating,
    status.seq,
    status.tick,
    composerText,
    onReload,
    refreshStatus,
  ]);

  if (!bgActive) return null;
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-28 z-10 flex justify-center">
      <div className="bg-muted/90 text-muted-foreground flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs shadow-sm backdrop-blur">
        <Loader2 className="size-3.5 animate-spin" />
        Агент работает в фоне…
      </div>
    </div>
  );
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

// PermissionDialog — human-in-the-loop: показывает запрос разрешения от агента и
// отправляет выбранное решение. Закрытие только через выбор варианта (без крестика),
// чтобы прогон не остался без ответа.
function PermissionDialog({
  permission,
  onDecide,
}: {
  permission: PendingPermission | null;
  onDecide: (decision: string) => void;
}) {
  return (
    <Dialog open={permission !== null}>
      <DialogContent showCloseButton={false} className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{permission?.title}</DialogTitle>
          <DialogDescription>
            Агент запрашивает разрешение на действие.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter className="flex-row justify-end gap-2">
          {permission?.options.map((o) => {
            const reject = o.kind?.startsWith("reject");
            return (
              <Button
                key={o.optionId}
                variant={reject ? "outline" : "default"}
                className={reject ? "text-destructive" : undefined}
                onClick={() => onDecide(o.optionId)}
              >
                {o.name ?? o.optionId}
              </Button>
            );
          })}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
