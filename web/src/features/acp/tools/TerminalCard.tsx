import { SquareTerminal, Loader2 } from "lucide-react";

// TerminalCard — выполнение команды (Bash/Terminal): вывод в терминальном стиле.
export function TerminalCard({
  output,
  command,
  title,
  state,
}: {
  output: string | null;
  command?: string;
  title?: string;
  state: "running" | "ok" | "error";
}) {
  return (
    <div className="overflow-hidden rounded-lg border bg-card/60">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs">
        <SquareTerminal className="size-3.5 shrink-0 text-muted-foreground" />
        <span className={command ? "min-w-0 truncate font-mono font-medium" : "min-w-0 truncate font-medium"}>{command || title || "Terminal"}</span>
        {state === "running" && (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}
      </div>
      {output !== null && (
        <pre className="max-h-72 overflow-auto bg-card/60 px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
          {output}
        </pre>
      )}
      {state !== "running" && (
        <div className={state === "ok" ? "border-t px-3 py-1.5 text-xs text-success" : "border-t px-3 py-1.5 text-xs text-destructive"}>
          {state === "ok" ? "✓ Выполнено" : "Ошибка выполнения"}
        </div>
      )}
    </div>
  );
}
