import { useCallback, useState } from "react";
import { ChevronDown, ChevronUp, RotateCw, SquareTerminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { TerminalView, type TermConnState } from "./TerminalView";

/**
 * ShellPanel — складная нижняя панель вспомогательного шелла сессии: параллельный
 * терминал рядом с агентом для ручного осмотра рабочей директории (local — шелл хоста
 * в cwd сессии, docker — exec в контейнер сессии).
 *
 * Шелл живёт ровно столько, сколько открыта панель: раскрытие монтирует TerminalView
 * (WS-подключение спавнит шелл на сервере), сворачивание размонтирует его (разрыв WS
 * завершает процесс). Повторное раскрытие даёт свежий шелл.
 */
export function ShellPanel({ sessionId }: { sessionId: string }) {
  const [open, setOpen] = useState(false);
  const [conn, setConn] = useState<TermConnState>("connecting");
  // Счётчик попыток переподключения: инкремент пересоздаёт WS (и шелл на сервере).
  const [attempt, setAttempt] = useState(0);

  const toggle = useCallback(() => {
    setOpen((v) => {
      if (!v) setConn("connecting");
      return !v;
    });
  }, []);

  return (
    <div className="shrink-0 border-t bg-background">
      <div className="flex h-9 items-center gap-2 px-3">
        <button
          type="button"
          onClick={toggle}
          className="flex min-w-0 flex-1 items-center gap-2 text-xs text-muted-foreground transition-colors hover:text-foreground"
          aria-expanded={open}
        >
          <SquareTerminal className="size-4 shrink-0" />
          <span className="truncate">Терминал</span>
          {open && <ShellConnDot conn={conn} />}
          <span className="ml-auto flex items-center">
            {open ? (
              <ChevronDown className="size-4" />
            ) : (
              <ChevronUp className="size-4" />
            )}
          </span>
        </button>
        {open && (conn === "closed" || conn === "error") && (
          <Button
            variant="outline"
            size="sm"
            className="h-6 gap-1.5 px-2 text-xs"
            onClick={() => {
              setConn("connecting");
              setAttempt((a) => a + 1);
            }}
          >
            <RotateCw className="size-3" />
            переподключить
          </Button>
        )}
      </div>
      {open && (
        <div className="h-[40vh]">
          <TerminalView
            kind="shell"
            sessionId={sessionId}
            attempt={attempt}
            onConnChange={setConn}
          />
        </div>
      )}
    </div>
  );
}

// ShellConnDot — точка состояния соединения шелла: зелёная (открыто), серая
// (подключение), красная (закрыто/ошибка).
function ShellConnDot({ conn }: { conn: TermConnState }) {
  const color =
    conn === "open"
      ? "bg-success"
      : conn === "connecting"
        ? "bg-muted-foreground"
        : "bg-destructive";
  return <span className={`size-1.5 shrink-0 rounded-full ${color}`} />;
}
