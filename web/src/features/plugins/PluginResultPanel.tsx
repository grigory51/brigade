import { useState } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { TerminalView, type TermConnState, type TerminalCommand } from "@/features/terminal/TerminalView";
import type { PluginResult } from "./host-tools";

export function PluginResultPanel({ sessionId, result, revision }: { sessionId: string; result: PluginResult; revision: number }) {
  const [run, setRun] = useState<{ command: TerminalCommand; attempt: number } | null>(null);
  const [connection, setConnection] = useState<TermConnState>("connecting");
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [loadedRevision, setLoadedRevision] = useState<number | null>(null);

  if (result.kind === "web" && !run) {
    if (new URL(result.url).origin === window.location.origin) {
      return (
        <div className="flex h-full flex-col items-start justify-center gap-3 p-5 text-sm">
          <p>Для встроенного preview нужен изолированный origin. Этот адрес использует origin Brigade, поэтому безопасное встраивание приложения недоступно.</p>
          <a href={result.url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 underline underline-offset-4"><ExternalLink aria-hidden="true" className="size-4" />Открыть внешне</a>
        </div>
      );
    }
    return (
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2 text-xs text-muted-foreground">
          <span className="min-w-0 flex-1 truncate">{result.url}</span>
          <a href={result.url} target="_blank" rel="noopener noreferrer" aria-label="Открыть preview в новой вкладке"><ExternalLink className="size-4" /></a>
        </div>
        <div className="relative min-h-0 flex-1">
          {loadedRevision === null && <div role="status" className="absolute inset-0 flex items-center justify-center gap-2 bg-background text-sm"><Loader2 aria-hidden="true" className="size-4 motion-safe:animate-spin" />Загрузка preview…</div>}
          <iframe key={revision} title={result.name} src={result.url} sandbox="allow-scripts allow-same-origin allow-forms allow-downloads" referrerPolicy="no-referrer" onLoad={() => setLoadedRevision(revision)} className="h-full w-full border-0" />
        </div>
      </div>
    );
  }

  if (!run && result.kind === "terminal") {
    return (
      <div className="flex h-full flex-col gap-4 overflow-auto p-5">
        <h2 className="text-sm font-medium">Запустить «{result.name}» в терминале?</h2>
        <p className="text-xs text-muted-foreground">Команда выполнится в окружении текущей сессии. Проверьте аргументы и рабочую директорию.</p>
        <pre className="overflow-x-auto rounded-md border bg-muted/30 p-3 text-xs">{JSON.stringify(result.command, null, 2)}</pre>
        <p className="text-xs text-muted-foreground">Смена вкладки сохраняет процесс. Закрытие результата, смена приложения или перезагрузка страницы закрывает shell. После перезагрузки команда сама не запускается.</p>
        <Button className="w-fit active:scale-[0.98] motion-reduce:transform-none" onClick={() => setRun({ command: result.command, attempt: 0 })}>Запустить</Button>
      </div>
    );
  }

  if (!run) return null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {result.kind === "terminal" && JSON.stringify(run.command) !== JSON.stringify(result.command) && (
        <div className="shrink-0 space-y-2 border-b p-3 text-xs">
          <p>Зарегистрирована новая команда. Перезапуск закроет текущий shell.</p>
          <pre className="max-h-28 overflow-auto">{JSON.stringify(result.command, null, 2)}</pre>
          <Button size="sm" variant="outline" onClick={() => { setExitCode(null); setConnection("connecting"); setRun({ command: result.command, attempt: run.attempt + 1 }); }}>Перезапустить с новой командой</Button>
        </div>
      )}
      {result.kind === "web" && (
        <div className="shrink-0 space-y-2 border-b p-3 text-xs">
          <p>Этот результат теперь содержит preview. Текущий shell продолжает работать.</p>
          <Button size="sm" variant="outline" onClick={() => setRun(null)}>Закрыть shell и открыть preview</Button>
        </div>
      )}
      <div role="status" className="shrink-0 border-b px-3 py-2 text-xs text-muted-foreground">
        {exitCode !== null ? `Команда завершена · exit code ${exitCode}` : connection === "connecting" ? "Подключение к терминалу…" : connection === "open" ? "Команда выполняется" : "Соединение закрыто; код завершения неизвестен"}
        <span className="ml-2">Закрытие вкладки результата завершает shell.</span>
      </div>
      <div className="min-h-0 flex-1">
        <TerminalView key={run.attempt} kind="shell" sessionId={sessionId} command={run.command} onConnChange={setConnection} onCommandExit={setExitCode} />
      </div>
    </div>
  );
}
