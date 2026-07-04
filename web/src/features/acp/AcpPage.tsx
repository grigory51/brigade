import {
  AssistantRuntimeProvider,
  useAuiState,
  useComposer,
} from "@assistant-ui/react";
import { useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
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
        <BackgroundActivity status={status} onReload={onReload} />
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
// когда фоновый turn завершился. «Фон» = агент генерирует (status.generating), но живого
// прогона в этом клиенте нет (thread.isRunning=false): вывод такого turn'а не попадает в
// тред живьём, только в history бэкенда. Перезагрузку откладываем, пока это небезопасно
// (идёт прогон или в поле ввода есть недописанный текст), чтобы ремоунт его не затёр.
function BackgroundActivity({
  status,
  onReload,
}: {
  status: AgentStatus;
  onReload: () => void;
}) {
  const isRunning = useAuiState((s) => s.thread.isRunning);
  const composerText = useComposer((c) => c.text);

  // idleSeq — seq ленты, до которого тред считается синхронизированным (последнее
  // «спокойное» наблюдение). Пока идёт фоновый turn, он заморожен на дофоновом значении —
  // так рост ленты за время turn'а корректно виден как «есть новые сообщения», даже если
  // быстрый turn успел вырасти между поллами.
  const idleSeq = useRef(status.seq);
  const sawBg = useRef(false);
  const wasRunning = useRef(isRunning);
  // cooldownTick — tick, на котором foreground-прогон завершился. Фоновую детекцию
  // включаем только со СЛЕДУЮЩЕГО полла (status.tick > cooldownTick): isRunning выключается
  // мгновенно по RUN_FINISHED (SSE), а кэш status.generating обновляется лишь поллингом
  // (до STATUS_POLL_MS позже) и в этом окне ещё stale-true — без «остывания» хвост
  // завершённого прогона ложно считался бы фоновой активностью (лишний ремоунт + мигание).
  const cooldownTick = useRef<number | null>(null);
  const [bgActive, setBgActive] = useState(false);

  useEffect(() => {
    if (isRunning) {
      // Foreground-прогон: его сообщения стримятся в тред живьём, база едет за seq.
      idleSeq.current = status.seq;
      sawBg.current = false;
      cooldownTick.current = null;
      wasRunning.current = true;
      setBgActive(false);
      return;
    }
    if (wasRunning.current) {
      // Прогон только что завершился — входим в остывание до свежего полла: пока не
      // доверяем status.generating (может быть stale-true из полла во время прогона).
      wasRunning.current = false;
      cooldownTick.current = status.tick;
      idleSeq.current = status.seq;
      setBgActive(false);
      return;
    }
    if (cooldownTick.current !== null) {
      if (status.tick <= cooldownTick.current) {
        // Тот же (возможно stale) полл — держим базу у seq, generating не доверяем.
        idleSeq.current = status.seq;
        setBgActive(false);
        return;
      }
      // Пришёл свежий полл после конца прогона — generating теперь достоверен.
      cooldownTick.current = null;
      idleSeq.current = status.seq;
      // проваливаемся в обычную логику ниже
    }

    const bgBusy = status.generating && !isRunning;
    if (bgBusy) {
      // Фоновый turn генерирует: помечаем и НЕ трогаем idleSeq (держим дофоновую базу).
      sawBg.current = true;
      setBgActive(true);
      return;
    }
    setBgActive(false);
    // Покой без активного прогона (turn завершён, generating=false).
    if (sawBg.current) {
      // Был фоновый turn. Перезагружаем историю (ремоунт рантайма подтянет ленту), когда
      // безопасно: лента выросла сверх дофоновой базы и поле ввода пусто (иначе ремоунт
      // затёр бы недописанное — ждём, sawBg остаётся, перезагрузим по очистке ввода).
      if (status.seq > idleSeq.current && composerText.trim() === "") {
        sawBg.current = false;
        idleSeq.current = status.seq;
        onReload();
      }
      return;
    }
    // Обычный покой — двигаем базу за лентой (foreground-сообщения уже в треде).
    idleSeq.current = status.seq;
  }, [
    isRunning,
    status.generating,
    status.seq,
    status.tick,
    composerText,
    onReload,
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
