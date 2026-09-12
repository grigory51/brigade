import { useEffect, useRef, useState } from "react";
import { useThreadRuntime, useAuiState } from "@assistant-ui/react";
import { Globe, Loader2 } from "lucide-react";
import { browserClient } from "@/api/client";
import { BrowserAction } from "@/api/gen/brigade/v1/browser_pb";
import { Button } from "@/components/ui/button";
import { BrowserWindow } from "../BrowserWindow/BrowserWindow";

export function browserRequestId(result: unknown): string | undefined {
  const text = typeof result === "string" ? result : JSON.stringify(result ?? "");
  return text.replace(/\\"/g, '"').match(/"browserRequestId"\s*:\s*"([0-9a-f-]{36})"/)?.[1];
}

export function BrowserHandoffCard({ sessionId, result, reason }: { sessionId?: string; result: unknown; reason?: string }) {
  const id = browserRequestId(result);
  const [open, setOpen] = useState(false);
  const [state, setState] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const thread = useThreadRuntime();
  const running = useAuiState(s => s.thread.isRunning);
  const submitted = useRef(false);

  useEffect(() => {
    if (!sessionId || !id) return;
    const controller = new AbortController();
    void browserClient.interact({ sessionId, requestId: id, action: BrowserAction.STATUS }, { signal: controller.signal })
      .then(r => setState(r.state))
      .catch(e => { if (!controller.signal.aborted) setError(String(e)); });
    return () => controller.abort();
  }, [sessionId, id]);

  const finish = async (cancel: boolean) => {
    if (!sessionId || !id || busy || running || submitted.current) return;
    setBusy(true); setError("");
    try {
      const response = await browserClient.interact({ sessionId, requestId: id, action: cancel ? BrowserAction.CANCEL : BrowserAction.RESUME });
      setState(response.state);
      if (response.state !== (cancel ? "cancelled" : "agent")) throw new Error("Запрос уже завершён или устарел. Попросите агента открыть браузер снова.");
      setOpen(false);
      await thread.append({ role: "user", content: [{ type: "text", text: cancel
        ? "Я отменил ручное действие в браузере. Не продолжай операцию, для которой требовались вход или подтверждение."
        : "Я завершил ручное действие в браузере. Продолжай в той же вкладке через browser/read." }] });
      submitted.current = true;
    } catch (e) { setError(String(e)); }
    finally { setBusy(false); }
  };

  return <section className="space-y-3 rounded-xl border bg-card p-4 text-sm">
    <div className="flex items-center gap-2 font-medium"><Globe className="size-4 text-primary" />Нужно ваше действие в браузере</div>
    <p className="text-muted-foreground">{reason || "Пройдите проверку или войдите на сайте, затем верните управление агенту."}</p>
    {!id ? <p className="text-muted-foreground">{result ? "Браузер не удалось подготовить. Подробности — в ответе агента." : "Подготавливаю браузер…"}</p> :
      !sessionId ? <p className="text-muted-foreground">Браузер архивной сессии недоступен.</p> :
      state === "expired" ? <p className="text-muted-foreground">Браузер закрыт или запрос устарел. Попросите агента открыть его снова.</p> :
      state === "cancelled" ? <p className="text-muted-foreground">Действие отменено.</p> :
      state === "agent" && !error ? <p className="text-muted-foreground">Управление возвращено агенту.</p> :
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => { setError(""); setOpen(true); }}>Открыть браузер</Button>
        {error && state === "agent" && <Button variant="outline" disabled={busy || running} onClick={() => void finish(false)}>Продолжить диалог</Button>}
      </div>}
    {error && <p role="alert" className="break-words text-destructive">{error}</p>}
    {sessionId && id && <BrowserWindow open={open} onOpenChange={setOpen} sessionId={sessionId} requestId={id}
      disabled={busy || running} onFinish={finish} onState={setState} />}
    {busy && <Loader2 className="size-4 animate-spin" aria-label="Сохраняю решение" />}
  </section>;
}
