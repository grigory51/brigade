import { useCallback, useEffect, useRef, useState } from "react";
import { Globe, Loader2, RefreshCw } from "lucide-react";
import { browserClient } from "@/api/client";
import { BrowserAction, type BrowserResponse } from "@/api/gen/brigade/v1/browser_pb";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

export function BrowserWindow({ open, onOpenChange, sessionId, requestId, disabled, onFinish, onState }: {
  open: boolean; onOpenChange: (open: boolean) => void; sessionId: string; requestId: string;
  disabled: boolean; onFinish: (cancel: boolean) => Promise<void>; onState: (state: string) => void;
}) {
  const [frame, setFrame] = useState<BrowserResponse>();
  const [image, setImage] = useState("");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const keyboard = useRef<HTMLTextAreaElement>(null);
  const surface = useRef<HTMLDivElement>(null);
  const composing = useRef(false);
  const queue = useRef(Promise.resolve());
  const controller = useRef<AbortController | null>(null);
  const lastMove = useRef(0);
  const inputFailed = useRef(false);

  useEffect(() => {
    if (!open) return;
    const abort = new AbortController(); controller.current = abort;
    let timer: ReturnType<typeof setTimeout>;
    let objectURL = "";
    inputFailed.current = false;
    setError(""); setFrame(undefined); setImage("");
    const poll = async () => {
      try {
        if (document.hidden) { timer = setTimeout(poll, 1000); return; }
        const response = await browserClient.interact({ sessionId, requestId, action: BrowserAction.FRAME }, { signal: abort.signal });
        if (abort.signal.aborted) return;
        onState(response.state); setFrame(response);
        if (response.state !== "human") return;
        const previous = objectURL;
        objectURL = URL.createObjectURL(new Blob([new Uint8Array(response.image)], { type: "image/jpeg" }));
        setImage(objectURL);
        if (previous) URL.revokeObjectURL(previous);
        timer = setTimeout(poll, 300);
      } catch (e) { if (!abort.signal.aborted) setError(String(e)); }
    };
    void poll();
    return () => { abort.abort(); clearTimeout(timer); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [open, sessionId, requestId, attempt, onState]);

  const input = useCallback((kind: string, x = 0, y = 0, text = "") => {
    const signal = controller.current?.signal;
    if (!signal || signal.aborted) return;
    queue.current = queue.current.then(async () => {
      if (signal.aborted) return;
      await browserClient.interact({ sessionId, requestId, action: BrowserAction.INPUT, input: kind, x, y, text }, { signal });
    }).catch(e => { if (!signal.aborted) { inputFailed.current = true; setError(String(e)); } });
  }, [sessionId, requestId]);

  useEffect(() => {
    const element = surface.current;
    if (!element) return;
    const wheel = (event: WheelEvent) => {
      event.preventDefault();
      input("wheel", Math.max(-3000, Math.min(3000, event.deltaX)), Math.max(-3000, Math.min(3000, event.deltaY)));
    };
    element.addEventListener("wheel", wheel, { passive: false });
    return () => element.removeEventListener("wheel", wheel);
  }, [!!image, !!error, open, input]);

  const pointer = (event: React.PointerEvent<HTMLDivElement>, kind: string) => {
    if (!frame || frame.state !== "human") return;
    if (kind === "down") {
      event.preventDefault(); event.currentTarget.setPointerCapture(event.pointerId);
      keyboard.current?.focus({ preventScroll: true });
    }
    if (kind === "move" && Date.now() - lastMove.current < 40) return;
    lastMove.current = Date.now();
    const bounds = event.currentTarget.getBoundingClientRect();
    input(kind, Math.max(0, Math.min(frame.width, (event.clientX - bounds.left) * frame.width / bounds.width)),
      Math.max(0, Math.min(frame.height, (event.clientY - bounds.top) * frame.height / bounds.height)));
    if (kind === "up" && event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
  };

  return <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent className="flex max-h-[95dvh] w-[calc(100%-1rem)] max-w-[1100px] flex-col gap-3 overflow-y-auto p-3 sm:max-w-[1100px] sm:p-5" onOpenAutoFocus={e => e.preventDefault()}>
      <DialogTitle className="flex items-center gap-2 pr-8 text-base"><Globe className="size-4" />Браузер сессии</DialogTitle>
      <DialogDescription>Сейчас управляете вы. Вход сохранится в браузере этой сессии и будет доступен агенту после продолжения.</DialogDescription>
      <div className="min-h-9 overflow-x-auto rounded-md border bg-muted px-3 py-2 font-mono text-xs select-text" aria-label="Адрес сайта">{frame?.url || "Подключаюсь…"}</div>
      {error ? <div role="alert" className="space-y-2 rounded-lg border border-destructive/30 p-4 text-sm"><p className="break-words text-destructive">{error}</p><Button variant="outline" onClick={() => setAttempt(n => n + 1)}><RefreshCw />Переподключиться</Button></div> :
        frame && frame.state !== "human" ? <p className="p-4 text-sm">{frame.state === "agent" ? "Управление уже возвращено агенту." : "Браузер закрыт или запрос устарел."}</p> :
        <div className="relative min-h-32 shrink-0 overflow-hidden rounded-lg border bg-muted">
          {!image ? <div className="flex h-48 items-center justify-center"><Loader2 className="size-6 animate-spin" aria-label="Загружаю браузер" /></div> :
            <div ref={surface} className="relative mx-auto w-full touch-none select-none" style={{ maxWidth: "max(240px, calc((95dvh - 18rem) * 1024 / 720))" }} aria-label="Удалённая страница" role="group"
              onPointerDown={e => pointer(e, "down")} onPointerMove={e => pointer(e, "move")} onPointerUp={e => pointer(e, "up")}
              onPointerCancel={e => pointer(e, "up")} onContextMenu={e => e.preventDefault()}>
              <img src={image} alt="Страница в браузере сессии" draggable={false} className="block w-full" />
            </div>}
          <textarea ref={keyboard} aria-label="Ввод в удалённый браузер" autoComplete="off" autoCorrect="off" autoCapitalize="off" spellCheck={false}
            className="absolute bottom-0 left-0 size-px resize-none opacity-0"
            onCompositionStart={() => { composing.current = true; }}
            onCompositionEnd={e => { composing.current = false; input("text", 0, 0, e.currentTarget.value); e.currentTarget.value = ""; }}
            onChange={e => { if (!composing.current && e.currentTarget.value) { input("text", 0, 0, e.currentTarget.value); e.currentTarget.value = ""; } }}
            onKeyDown={e => {
              if (composing.current || e.nativeEvent.isComposing || e.key === "Escape") return;
              if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) return;
              if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "v") return;
              if (["Shift", "Control", "Meta", "Alt"].includes(e.key)) return;
              e.preventDefault();
              const modifiers = [e.ctrlKey && "Control", e.metaKey && "Meta", e.altKey && "Alt", e.shiftKey && "Shift"].filter(Boolean);
              input("press", 0, 0, [...modifiers, e.key].join("+"));
            }}
            onPaste={e => { e.preventDefault(); input("text", 0, 0, e.clipboardData.getData("text/plain")); }} />
        </div>}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" className="hidden [@media(any-pointer:coarse)]:inline-flex" onClick={() => keyboard.current?.focus()} title="Показать клавиатуру телефона для ввода в выбранное поле сайта">Показать клавиатуру</Button>
        <Button variant="outline" onClick={() => input("press", 0, 0, "Tab")}>Tab</Button>
        <Button variant="outline" onClick={() => input("press", 0, 0, "Enter")}>Enter</Button>
        <Button variant="outline" onClick={() => input("wheel", 0, 450)}>↓</Button>
        <Button variant="outline" onClick={() => input("wheel", 0, -450)}>↑</Button>
        <div className="ml-auto flex gap-2">
          <Button variant="ghost" disabled={disabled} onClick={() => void onFinish(true)}>Отменить</Button>
          <Button disabled={disabled || frame?.state !== "human" || !!error} onClick={async () => { await queue.current; if (!inputFailed.current) await onFinish(false); }}>Продолжить</Button>
        </div>
      </div>
      <p className="text-xs text-muted-foreground">Нажмите поле на странице и печатайте — ввод отправляется на сайт, не в чат.</p>
      {disabled && <p className="text-xs text-muted-foreground">Дождитесь завершения текущего ответа агента.</p>}
    </DialogContent>
  </Dialog>;
}
