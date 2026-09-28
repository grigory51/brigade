import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Bot, Check, ChevronUp, Loader2, MessageSquarePlus, PenLine, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { acpClient, sessionClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { DraftComposer } from "./components/DraftComposer/DraftComposer";
import { BusinessConversation } from "./components/BusinessConversation/BusinessConversation";
import { canUseAsContext, selectedMessageIds, type ContextSelection } from "./contextSelection";

type Run = { id: string; status: string; error: string };
const INITIAL_VISIBLE = 30;

export function ShadowSession({ sessionId }: { sessionId: string }) {
  const [messages, setMessages] = useState<AcpMessage[] | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [note, setNote] = useState("");
  const [noteOpen, setNoteOpen] = useState(false);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState("");
  const [loadError, setLoadError] = useState(false);
  const [selection, setSelection] = useState<ContextSelection>({ kind: "recent", count: 10 });
  const [visibleCount, setVisibleCount] = useState(INITIAL_VISIBLE);
  const [activeDraftId, setActiveDraftId] = useState("");
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
      if (!active) return;
      try {
        await refresh();
      } catch {
        if (active) setLoadError(true);
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, [refresh]);

  const act = useCallback(async (name: string, action: () => Promise<unknown>): Promise<boolean> => {
    setBusy(name);
    let success = false;
    try {
      await action();
      success = true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Не удалось выполнить действие");
    } finally {
      try { await refresh(); } catch { setLoadError(true); }
      setBusy("");
    }
    return success;
  }, [refresh]);

  const eligible = useMemo(() => messages?.filter(canUseAsContext) ?? [], [messages]);
  const selectedIds = useMemo(() => selectedMessageIds(messages ?? [], selection), [messages, selection]);
  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const contextBytes = useMemo(() => {
    const encoder = new TextEncoder();
    return (messages ?? []).reduce((bytes, message) => bytes + (selectedSet.has(message.id) ? encoder.encode(message.content).length + 300 : 0), 0);
  }, [messages, selectedSet]);
  const drafts = messages?.filter((message) => message.delivery === "draft") ?? [];
  const pendingDraft = drafts.find((message) => message.id === activeDraftId) ?? drafts.at(-1) ?? null;
  const uncertainDraft = [...(messages ?? [])].reverse().find((message) => message.delivery === "uncertain") ?? null;
  const businessContacts = (messages ?? []).filter((message) => message.author === "contact" && message.source.startsWith("telegram-business/"));
  const latestContact = businessContacts.at(-1) ?? null;
  const replyTarget = businessContacts.find((message) => message.id === pendingDraft?.replyToId) ?? latestContact;
  const targetMissingFromContext = latestContact !== null && !selectedSet.has(latestContact.id);
  const earlierCount = Math.max(0, (messages?.length ?? 0) - visibleCount);
  const visibleMessages = messages?.slice(-visibleCount).filter((message) => message.id !== pendingDraft?.id) ?? [];
  const isBusiness = messages?.some((message) => message.source.startsWith("telegram-business/")) ?? false;

  function toggleMessage(messageId: string) {
    const next = new Set(selectedIds);
    if (next.has(messageId)) next.delete(messageId);
    else next.add(messageId);
    setSelection({ kind: "custom", ids: next });
  }

  async function addNote() {
    const content = note.trim();
    if (!content) return;
    const saved = await act("note", () => sessionClient.addMessage({ sessionId, content }));
    if (saved) { setNote(""); setNoteOpen(false); }
  }

  async function createReply() {
    if (!latestContact || !reply.trim()) return;
    const created = await act("reply", async () => {
      const result = await sessionClient.createDraft({ sessionId, content: reply.trim(), replyToMessageId: latestContact.id });
      setActiveDraftId(result.messageId);
    });
    if (created) setReply("");
  }

  if (isBusiness && messages) {
    return <BusinessConversation sessionId={sessionId} messages={messages} run={run} busy={busy} act={act} loadError={loadError} />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <header className="shrink-0 border-b px-4 py-3 sm:px-5">
        <div className="mx-auto max-w-3xl">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm font-medium">
            <Bot className="size-4 text-primary" /> {isBusiness ? "Business-переписка" : "Переписка"}
            <span className="ml-auto text-xs font-normal text-muted-foreground">Агент запускается только по вашей команде</span>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">Контекст следующего ответа</p>
              <p className="text-xs text-muted-foreground" aria-live="polite">
                Выбрано {selectedIds.length} из {eligible.length} сообщений. История при этом не меняется.
              </p>
            </div>
            <div className="flex flex-wrap gap-1" role="group" aria-label="Быстрый выбор контекста">
              <span className="self-center px-1 text-xs text-muted-foreground">Последние</span>
              {[5, 10, 20].map((count) => (
                <Button key={count} size="sm" variant={selection.kind === "recent" && selection.count === count ? "secondary" : "ghost"}
                  className="min-h-11 min-w-11 sm:min-h-8" aria-label={`Последние ${count} сообщений`} aria-pressed={selection.kind === "recent" && selection.count === count}
                  onClick={() => setSelection({ kind: "recent", count })}>{count}</Button>
              ))}
              <Button size="sm" className="min-h-11 sm:min-h-8" variant={selection.kind === "all" ? "secondary" : "ghost"} aria-pressed={selection.kind === "all"}
                onClick={() => setSelection({ kind: "all" })}>Все</Button>
              {selection.kind === "custom" && <span className="self-center rounded-md bg-secondary px-2 py-1.5 text-xs text-secondary-foreground">Вручную</span>}
            </div>
          </div>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto px-4 py-5" aria-busy={messages === null && !loadError}>
        <div className="mx-auto max-w-3xl space-y-3">
          {messages === null && !loadError && <div className="flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Загружаем переписку…</div>}
          {loadError && <div className="rounded-lg border border-destructive/50 p-4 text-sm text-destructive" role="alert">Не удалось обновить переписку. Повторяем подключение; уже загруженные сообщения доступны.</div>}
          {messages?.length === 0 && <div className="py-16 text-center text-sm text-muted-foreground">Здесь появятся сообщения. Можно добавить заметку или подключить Telegram Business.</div>}
          {earlierCount > 0 && <Button variant="ghost" className="w-full text-muted-foreground" onClick={() => setVisibleCount((count) => count + 50)}>
            <ChevronUp className="size-4" /> Показать более ранние сообщения ({earlierCount})
          </Button>}
          {visibleMessages.map((message) => {
            const selectable = canUseAsContext(message);
            const included = selectedSet.has(message.id);
            if (message.delivery === "draft") {
              return <div key={message.id} className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-primary/30 bg-primary/5 p-3 text-sm">
                <span className="min-w-0 flex-1 truncate">Черновик: {message.content}</span>
                <Button size="sm" className="min-h-11 sm:min-h-8" variant="outline" onClick={() => setActiveDraftId(message.id)}>Открыть черновик</Button>
              </div>;
            }
            return (
              <article key={message.id} className={cn("rounded-xl border p-3 sm:p-4", message.author === "contact" ? "bg-card/50" : "bg-background")}>
                <div className="mb-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span className="font-medium text-foreground">{message.author === "contact" ? "Собеседник" : message.author === "owner" ? "Вы" : "Агент"}</span>
                  {message.source.startsWith("telegram-business/") && <span>· Telegram</span>}
                  {message.delivery === "sent" && <span>· отправлено</span>}
                  {message.delivery === "uncertain" && <span className="text-warning">· доставка неизвестна</span>}
                  {message.delivery === "failed" && <span className="text-destructive">· отправка отклонена</span>}
                  {selectable && <button type="button" aria-pressed={included} aria-label={`${included ? "Исключить из контекста" : "Добавить в контекст"}: ${message.content.slice(0, 60)}`}
                    className={cn("ml-auto min-h-11 rounded-full border px-3 text-xs transition-colors focus-visible:ring-2 focus-visible:ring-ring sm:min-h-8", included ? "border-primary/45 bg-primary/10 text-foreground" : "text-muted-foreground hover:text-foreground")}
                    onClick={() => toggleMessage(message.id)}>
                    {included && <Check className="mr-1 inline size-3" />}{included ? "Учитывается" : "Добавить в контекст"}
                  </button>}
                </div>
                <Markdown className="wrap-break-word text-sm leading-relaxed">{message.content}</Markdown>
              </article>
            );
          })}
        </div>
      </main>

      {messages !== null && <footer className="max-h-[min(65dvh,600px)] shrink-0 overflow-y-auto border-t bg-background px-4 py-3 sm:py-4">
        <div className="mx-auto flex max-w-3xl flex-col gap-3">
          {run?.status === "running" && <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status"><Loader2 className="size-4 animate-spin" /> Агент готовит черновик…</div>}
          {run?.status === "failed" && <div className="rounded-lg border border-destructive/40 p-3 text-sm text-destructive" role="alert">Не удалось подготовить черновик: {run.error}</div>}
          {run?.status === "interrupted" && <p className="text-sm text-muted-foreground">Подготовка прервана. Можно запустить повторно.</p>}
          {uncertainDraft && !pendingDraft && <div className="rounded-lg border border-warning/40 p-3 text-sm text-warning" role="alert">Результат отправки неизвестен. Проверьте Telegram перед новой отправкой: этот черновик нельзя отправить повторно.</div>}
          {pendingDraft && <DraftComposer key={pendingDraft.id} message={pendingDraft} sessionId={sessionId} replyTarget={replyTarget} latestContact={latestContact} busy={busy} act={act} />}
          {!pendingDraft && isBusiness && latestContact && <section className="space-y-2" aria-label="Ручной ответ">
            <label htmlFor="shadow-reply" className="text-sm font-medium">Ответить собеседнику</label>
            <Textarea id="shadow-reply" value={reply} onChange={(event) => setReply(event.target.value)} placeholder="Напишите ответ…" className="min-h-24 max-h-44 resize-y text-base sm:text-sm" />
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs text-muted-foreground">Сначала сохранится черновик. В Telegram он уйдёт только после подтверждения.</p>
              <Button disabled={!reply.trim() || busy !== ""} onClick={() => void createReply()}>
                {busy === "reply" ? <Loader2 className="size-4 animate-spin" /> : <PenLine className="size-4" />} Продолжить к отправке
              </Button>
            </div>
          </section>}
          {(!isBusiness || noteOpen) && <div className="space-y-2">
            <label htmlFor="shadow-note" className="text-xs font-medium text-muted-foreground">Заметка для этой переписки</label>
            <Textarea id="shadow-note" value={note} onChange={(event) => setNote(event.target.value)} placeholder="Что важно учесть в следующем ответе…" className="min-h-16 max-h-32 resize-y text-base sm:text-sm" />
            <p className="text-xs text-muted-foreground">Заметка останется в Brigade. Она попадёт в ответ, только если выбрана в контексте.</p>
          </div>}
          <div className="flex flex-wrap items-center justify-between gap-2">
            {isBusiness && !noteOpen ? <Button variant="ghost" onClick={() => setNoteOpen(true)}><MessageSquarePlus className="size-4" /> Добавить заметку</Button> :
              <Button variant="outline" disabled={!note.trim() || busy !== ""} onClick={() => void addNote()}>
                {busy === "note" ? <Loader2 className="size-4 animate-spin" /> : <MessageSquarePlus className="size-4" />} Добавить заметку
              </Button>}
            <Button variant="outline" disabled={busy !== "" || run?.status === "running" || selectedIds.length === 0 || targetMissingFromContext}
              onClick={() => { setActiveDraftId(""); void act("run", () => sessionClient.generateDraft({ sessionId, selectedMessageIds: selectedIds })); }}>
              {busy === "run" ? <Loader2 className="size-4 animate-spin" /> : <Sparkles className="size-4" />}
              {pendingDraft ? "Новый черновик" : "Подготовить ответ"}
            </Button>
          </div>
          {selectedIds.length === 0 && <p className="text-xs text-warning" role="status">Выберите хотя бы одно сообщение для подготовки ответа.</p>}
          {contextBytes > 112 * 1024 && <p className="text-xs text-warning" role="status">Контекст занимает около {Math.ceil(contextBytes / 1024)} КБ. Лимит запуска — 128 КБ; если не поместится, выберите меньше сообщений.</p>}
          {latestContact && targetMissingFromContext && <div className="flex flex-wrap items-center gap-2 text-xs text-warning" role="status">
            <span>Для ответа нужно учесть последнее входящее сообщение.</span>
            <button type="button" className="underline underline-offset-2" onClick={() => toggleMessage(latestContact.id)}>Добавить в контекст</button>
          </div>}
        </div>
      </footer>}
    </div>
  );
}
