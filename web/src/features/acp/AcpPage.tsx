import { AssistantRuntimeProvider } from "@assistant-ui/react";
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
import { useAcpRuntime, type PendingPermission } from "./useAcpRuntime";

// AcpSession монтируется из SessionGuard только при найденной сессии — иначе
// AG-UI-рантайм поднял бы соединение в никуда ещё до показа 404. Внешней шапки нет:
// идентификатор сессии доступен из URL, а тред показывает диалог сразу под навигацией.
export function AcpSession({ sessionId }: { sessionId: string }) {
  const { runtime, permission, resolvePermission, commands, plan } =
    useAcpRuntime(sessionId);

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      {/* Регистрация frontend-tool show_choice в model-context рантайма; уходит в
          RunAgentInput.tools[] при следующем прогоне. */}
      <AcpToolUI />

      <div className="flex h-full flex-col">
        <div className="min-h-0 flex-1">
          <AcpThread commands={commands} plan={plan} />
        </div>
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
