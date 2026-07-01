import { SquareTerminal, Loader2 } from "lucide-react";

// TerminalCard — выполнение команды (Bash/Terminal): вывод в терминальном стиле.
// Сама командная строка через ACP-адаптер не приходит (RawInput пуст), поэтому
// заголовок ограничен человекочитаемым описанием инструмента.
export function TerminalCard({
  output,
  running,
}: {
  output: string | null;
  running: boolean;
}) {
  return (
    <div className="overflow-hidden rounded-lg border bg-card/60">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs">
        <SquareTerminal className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">Terminal</span>
        {running && (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}
      </div>
      {output !== null && (
        <pre className="max-h-72 overflow-auto bg-zinc-950 px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all text-zinc-200">
          {output}
        </pre>
      )}
    </div>
  );
}
