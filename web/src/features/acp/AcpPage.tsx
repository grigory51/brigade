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

  const bgBusy = status.generating && !isRunning;

  const wasBg = useRef(false);
  const seqAtStart = useRef(status.seq);
  const pendingReload = useRef(false);

  useEffect(() => {
    if (bgBusy) {
      if (!wasBg.current) {
        wasBg.current = true;
        seqAtStart.current = status.seq;
      }
    } else if (wasBg.current) {
      wasBg.current = false;
      // Фоновый turn закончился: если лента выросла — появились новые сообщения к показу.
      if (status.seq > seqAtStart.current) pendingReload.current = true;
    }
    // Перезагружаем только когда безопасно: нет активного прогона и пусто в поле ввода.
    if (pendingReload.current && !isRunning && composerText.trim() === "") {
      pendingReload.current = false;
      onReload();
    }
  }, [bgBusy, status.seq, isRunning, composerText, onReload]);

  if (!bgBusy) return null;
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
