import { useState } from "react";
import { RotateCw } from "lucide-react";
import { TerminalView, type TermConnState } from "@/features/terminal/TerminalView";
import { FloatingWindow } from "./FloatingWindow";

/**
 * TerminalWindow — плавающее окно вспомогательного шелла сессии снизу справа
 * (local — шелл хоста в cwd сессии, docker — exec в контейнер). Шелл живёт ровно
 * столько, сколько открыто окно: закрытие размонтирует TerminalView и разрывает WS,
 * что завершает процесс на сервере.
 */
export function TerminalWindow({
  sessionId,
  onClose,
}: {
  sessionId: string;
  onClose: () => void;
}) {
  const [conn, setConn] = useState<TermConnState>("connecting");
  // Счётчик попыток переподключения: инкремент пересоздаёт WS (и шелл на сервере).
  const [attempt, setAttempt] = useState(0);

  return (
    <FloatingWindow
      className="right-3 bottom-5 z-20 h-[300px] max-h-[calc(100%-2.5rem)] w-[620px] max-w-[calc(100%-1.5rem)] rounded-[12px] lg:right-[66px]"
      titlebar={
        <div className="flex h-[34px] shrink-0 items-center gap-2 border-b border-border bg-background px-3">
          {/* Светофор в стиле macOS: активна только красная точка — закрывает окно. */}
          <span className="flex gap-1.5">
            <button
              type="button"
              onClick={onClose}
              aria-label="Закрыть терминал"
              className="size-[11px] rounded-full bg-destructive"
            />
            <span className="size-[11px] rounded-full bg-warning/70" />
            <span className="size-[11px] rounded-full bg-success/70" />
          </span>
          <span className="flex-1 text-center text-xs text-muted-foreground">
            Терминал — {sessionId.slice(0, 8)}
          </span>
          {(conn === "closed" || conn === "error") && (
            <button
              type="button"
              onClick={() => {
                setConn("connecting");
                setAttempt((a) => a + 1);
              }}
              className="flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              <RotateCw className="size-3" />
              переподключить
            </button>
          )}
        </div>
      }
    >
      <TerminalView
        kind="shell"
        sessionId={sessionId}
        attempt={attempt}
        onConnChange={setConn}
      />
    </FloatingWindow>
  );
}
