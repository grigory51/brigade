import { useCallback, useEffect, useRef, useState } from "react";
import { Bot, Check, Loader2, Send, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { acpClient, sessionClient, telegramClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

type Run = { id: string; status: string; error: string };

export function ShadowSession({ sessionId }: { sessionId: string }) {
  const [messages, setMessages] = useState<AcpMessage[] | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState("");
  const [loadError, setLoadError] = useState(false);
  const revision = useRef<bigint | null>(null);

  const refresh = useCallback(async () => {
    const [status, latest] = await Promise.all([
      acpClient.getStatus({ threadId: sessionId }),
      sessionClient.getDraftRun({ sessionId }),
    ]);
    if (revision.current !== status.seq) {
      const history = await acpClient.getHistory({ threadId: sessionId });
      revision.current = status.seq;
      setMessages(history.messages);
    }
    setRun(latest.runId ? { id: latest.runId, status: latest.status, error: latest.error } : null);
    setLoadError(false);
  }, [sessionId]);

  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        if (!active) return;
        await refresh();
      } catch {
        if (active) setLoadError(true);
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, [refresh]);

  async function act(name: string, action: () => Promise<unknown>) {
    setBusy(name);
    try {
      await action();
      await refresh();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Не удалось выполнить действие");
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="border-b px-5 py-4">
        <div className="mx-auto flex max-w-3xl items-center gap-2 text-sm font-medium">
          <Bot className="size-4 text-primary" /> Переписка
          <span className="ml-auto text-xs font-normal text-muted-foreground">Агент запускается только по вашей команде</span>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6">
        <div className="mx-auto max-w-3xl space-y-5">
          {messages === null && !loadError && <div className="flex justify-center py-16"><Loader2 className="size-5 animate-spin text-muted-foreground" /></div>}
          {loadError && <div className="rounded-lg border p-5 text-sm text-destructive">Не удалось загрузить переписку. Повторяем подключение…</div>}
          {messages?.length === 0 && <div className="py-16 text-center text-sm text-muted-foreground">Здесь появятся сообщения. Можно добавить заметку или подключить Telegram Business.</div>}
          {messages?.map((message) => message.delivery === "draft" || message.delivery === "sending" || message.delivery === "failed" || message.delivery === "uncertain" ? (
            <DraftCard key={message.id} message={message} sessionId={sessionId} busy={busy} act={act} />
          ) : (
            <div key={message.id} className={`rounded-xl border p-4 ${message.author === "contact" ? "border-border bg-card/50" : "ml-6 border-primary/20 bg-primary/5"}`}>
              <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
                <span className="font-medium text-foreground">{message.author === "contact" ? "Собеседник" : message.author === "owner" ? "Вы" : "Агент"}</span>
                {message.source.startsWith("telegram-business/") && <span>· Telegram</span>}
                {message.delivery === "sent" && <span>· отправлено</span>}
                <button type="button" className="ml-auto underline-offset-2 hover:underline" disabled={busy !== ""} onClick={() => void act(message.id, () => sessionClient.setMessageIncluded({ sessionId, messageId: message.id, included: !message.includedInContext }))}>
                  {message.includedInContext ? "В контексте" : "Исключено"}
                </button>
              </div>
              <Markdown className="wrap-break-word">{message.content}</Markdown>
            </div>
          ))}
          {run?.status === "running" && <div className="flex items-center gap-2 rounded-xl border border-primary/30 p-4 text-sm"><Loader2 className="size-4 animate-spin" /> Агент готовит черновик…</div>}
          {run?.status === "failed" && <div className="rounded-xl border border-destructive/40 p-4 text-sm text-destructive">Не удалось подготовить черновик: {run.error}</div>}
          {run?.status === "interrupted" && <div className="rounded-xl border p-4 text-sm text-muted-foreground">Подготовка черновика прервана. Можно запустить повторно.</div>}
        </div>
      </div>
      <div className="border-t px-4 py-4">
        <div className="mx-auto flex max-w-3xl flex-col gap-3">
          <Textarea value={note} onChange={(event) => setNote(event.target.value)} placeholder="Добавить свою заметку в переписку…" className="min-h-20 resize-y" />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs text-muted-foreground">Заметка останется только в Brigade</span>
            <div className="flex gap-2">
              <Button variant="outline" disabled={!note.trim() || busy !== ""} onClick={() => void act("note", async () => { await sessionClient.addMessage({ sessionId, content: note }); setNote(""); })}>
                {busy === "note" && <Loader2 className="size-4 animate-spin" />} Добавить заметку
              </Button>
              <Button disabled={busy !== "" || run?.status === "running" || !messages?.length} onClick={() => void act("run", () => sessionClient.generateDraft({ sessionId }))}>
                {busy === "run" ? <Loader2 className="size-4 animate-spin" /> : <Sparkles className="size-4" />} Подготовить ответ
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function DraftCard({ message, sessionId, busy, act }: {
  message: AcpMessage;
  sessionId: string;
  busy: string;
  act: (name: string, action: () => Promise<unknown>) => Promise<void>;
}) {
  const [text, setText] = useState(message.content);
  const [confirming, setConfirming] = useState(false);
  const changed = text.trim() !== message.content.trim();
  return (
    <div className="rounded-xl border border-primary/40 bg-primary/5 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Sparkles className="size-4 text-primary" /> Черновик агента</div>
      <Textarea value={text} onChange={(event) => { setText(event.target.value); setConfirming(false); }} disabled={message.delivery !== "draft"} className="min-h-28 resize-y bg-background" />
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{message.delivery === "uncertain" ? "Результат отправки неизвестен — проверьте Telegram. Повторная отправка заблокирована." : message.delivery === "failed" ? "Отправка отклонена Telegram." : "Не отправлен. Проверьте текст перед отправкой."}</span>
        <div className="flex gap-2">
          <Button variant="outline" disabled={!changed || !text.trim() || busy !== "" || message.delivery !== "draft"} onClick={() => void act(message.id, () => sessionClient.editDraft({ sessionId, messageId: message.id, content: text }))}>
            <Check className="size-4" /> Сохранить
          </Button>
          {message.replyToId && message.delivery === "draft" && <Button disabled={busy !== "" || changed} variant={confirming ? "destructive" : "default"} onClick={() => {
            if (!confirming) { setConfirming(true); return; }
            setConfirming(false);
            void act(message.id, () => telegramClient.sendDraft({ sessionId, messageId: message.id }));
          }}>
            {busy === message.id ? <Loader2 className="size-4 animate-spin" /> : <Send className="size-4" />}
            {confirming ? "Подтвердить отправку" : "Отправить в Telegram"}
          </Button>}
        </div>
      </div>
    </div>
  );
}
