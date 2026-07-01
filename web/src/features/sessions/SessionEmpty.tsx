import { MessagesSquare, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useSessionShell } from "./SessionLayout";

// SessionEmpty — пустое состояние области контента: показывается на /sessions, когда
// ни одна сессия не выбрана. Предлагает выбрать сессию в списке слева или создать
// новую тем же диалогом, что и кнопка в шапке sidebar.
export function SessionEmpty() {
  const { openCreate } = useSessionShell();

  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
      <div className="flex size-14 items-center justify-center rounded-full bg-muted">
        <MessagesSquare className="size-6 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <p className="font-medium">Сессия не выбрана</p>
        <p className="max-w-sm text-sm text-muted-foreground">
          Выберите сессию в списке слева или создайте новую, чтобы запустить
          агента.
        </p>
      </div>
      <Button size="sm" onClick={openCreate}>
        <Plus className="size-4" />
        Новая сессия
      </Button>
    </div>
  );
}
