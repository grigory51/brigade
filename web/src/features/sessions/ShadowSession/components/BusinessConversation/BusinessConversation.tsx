import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDownToLine, ArrowUp, ChevronUp, Clock3, Copy, CornerDownRight, GripHorizontal, Loader2, LockKeyhole, Pencil, Reply, Send, Sparkles, StickyNote, TriangleAlert, Undo2, X } from "lucide-react";
import { toast } from "sonner";
import { sessionClient, telegramClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import type { TelegramBot } from "@/api/gen/brigade/v1/telegram_pb";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { canUseAsContext } from "../../contextSelection";

type Run = { id: string; status: string; error: string } | null;
type PendingSend = { draftId: string | null; text: string; targetId: string; seconds: number };
type Props = {
  sessionId: string;
  messages: AcpMessage[];
  run: Run;
  busy: string;
  loadError: boolean;
  act: (name: string, action: () => Promise<unknown>) => Promise<boolean>;
};

const INITIAL_VISIBLE = 40;
const pluralRules = new Intl.PluralRules("ru-RU");
const plural = (count: number, forms: [string, string, string]) => forms[pluralRules.select(count) === "one" ? 0 : pluralRules.select(count) === "few" ? 1 : 2];

export function BusinessConversation({ sessionId, messages, run, busy, loadError, act }: Props) {
  const [bot, setBot] = useState<TelegramBot | null | undefined>();
  const [sessionName, setSessionName] = useState("");
  const [boundaryId, setBoundaryId] = useState("");
  const [targetId, setTargetId] = useState("");
  const [mode, setMode] = useState<"reply" | "note">("reply");
  const [text, setText] = useState("");
  const [editingDraftId, setEditingDraftId] = useState("");
  const [hiddenDraftIds, setHiddenDraftIds] = useState<ReadonlySet<string>>(new Set());
  const [pending, setPending] = useState<PendingSend | null>(null);
  const [visibleCount, setVisibleCount] = useState(INITIAL_VISIBLE);
  const [now, setNow] = useState(Date.now());
  const scroller = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const sending = useRef(false);

  const botId = messages.find((message) => message.source.startsWith("telegram-business/"))?.source.split("/")[1] ?? "";
  useEffect(() => {
    let active = true;
    void telegramClient.listBots({}).then((result) => { if (active) setBot(result.bots.find((item) => item.id === botId) ?? null); }).catch(() => { if (active) setBot(null); });
    void sessionClient.get({ sessionId }).then((result) => { if (active) setSessionName(result.session?.name ?? ""); }).catch(() => {});
    return () => { active = false; };
  }, [botId, sessionId]);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);

  const contacts = useMemo(() => messages.filter((message) => message.author === "contact" && message.source.startsWith("telegram-business/")), [messages]);
  const latestContact = contacts.at(-1) ?? null;
  const target = contacts.find((message) => message.id === targetId) ?? latestContact;
  const lastSentIndex = messages.reduce((index, message, position) => message.author === "owner" && message.delivery === "sent" ? position : index, -1);
  const today = new Date(now).toDateString();
  const firstTodayIndex = messages.findIndex((message) => new Date(Number(message.createdAt) * 1000).toDateString() === today);
  const defaultBoundary = messages[Math.min(messages.length - 1, lastSentIndex >= 0 ? lastSentIndex + 1 : Math.max(0, firstTodayIndex))]?.id ?? "";
  const boundary = messages.findIndex((message) => message.id === (boundaryId || defaultBoundary));
  const selected = useMemo(() => messages.slice(Math.max(0, boundary)).filter(canUseAsContext), [messages, boundary]);
  const contextIds = useMemo(() => selected.map((message) => message.id), [selected]);
  const selectedCount = selected.filter((message) => message.author !== "owner" || message.source !== "brigade").length;
  const noteCount = selected.length - selectedCount;
  const contextBytes = useMemo(() => new TextEncoder().encode(JSON.stringify(selected)).length, [selected]);
  const drafts = messages.filter((message) => message.delivery === "draft" && !hiddenDraftIds.has(message.id));
  const draft = drafts.at(-1) ?? null;
  const latestOutgoing = [...messages].reverse().find((message) => message.author === "owner" && message.delivery !== "draft");
  const shown = useMemo(() => messages.slice(-visibleCount).filter((message) => message.delivery !== "draft"), [messages, visibleCount]);
  const earlierCount = Math.max(0, messages.length - visibleCount);
  const canReply = bot?.businessCanReply === true;
  const sendDelay = bot?.sendDelaySeconds ?? 5;
  const replyExpiresAt = latestContact ? Number(latestContact.createdAt) * 1000 + 24 * 60 * 60 * 1000 : 0;
  const replyOpen = canReply && replyExpiresAt > now;

  useEffect(() => {
    if (bot && !replyOpen && !text) setMode("note");
  }, [bot, replyOpen, text]);
  const person = sessionName.replace(/^Telegram\s*·\s*/, "") || "собеседнику";
  const targetText = target?.content.replace(/\s+/g, " ").slice(0, 60) || "сообщение";
  const pendingTargetText = contacts.find((message) => message.id === pending?.targetId)?.content.replace(/\s+/g, " ").slice(0, 60) || targetText;

  useEffect(() => {
    const element = scroller.current;
    if (element && atBottom.current) element.scrollTop = element.scrollHeight;
  }, [messages.length, run?.status, pending?.seconds]);

  useEffect(() => {
    if (!pending) return;
    if (pending.seconds > 0) {
      const timer = window.setTimeout(() => setPending((current) => current && current.seconds > 0 ? { ...current, seconds: current.seconds - 1 } : current), 1000);
      return () => window.clearTimeout(timer);
    }
    if (sending.current) return;
    sending.current = true;
    const delivery = pending;
    void act("send", async () => {
      let draftId = delivery.draftId;
      if (draftId) {
        const original = messages.find((message) => message.id === draftId);
        if (original?.content !== delivery.text) await sessionClient.editDraft({ sessionId, messageId: draftId, content: delivery.text });
      } else {
        const created = await sessionClient.createDraft({ sessionId, content: delivery.text, replyToMessageId: delivery.targetId });
        draftId = created.messageId;
      }
      await telegramClient.sendDraft({ sessionId, messageId: draftId, replyToMessageId: delivery.targetId });
    }).then((sent) => {
      if (sent) {
        setText("");
        setEditingDraftId("");
        toast.success("Ответ отправлен в Telegram");
      } else if (!delivery.draftId) {
        setText(delivery.text);
      }
      setPending(null);
      sending.current = false;
    });
  }, [act, messages, pending, sessionId]);

  function queueSend(value: string, draftId: string | null, replyToId = target?.id) {
    if (!replyToId || !replyOpen || !value.trim() || [...value].length > 4096 || pending) return;
    setPending({ draftId, text: value.trim(), targetId: replyToId, seconds: sendDelay });
    atBottom.current = true;
  }

  async function submitComposer() {
    if (!text.trim() || busy || pending) return;
    if (mode === "note") {
      const saved = await act("note", () => sessionClient.addMessage({ sessionId, content: text.trim() }));
      if (saved) setText("");
      return;
    }
    queueSend(text, editingDraftId || null);
  }

  async function generate() {
    if (!contextIds.length || (latestContact && !contextIds.includes(latestContact.id))) {
      toast.error("Включите последнее входящее сообщение в контекст");
      return;
    }
    if (contextBytes > 128 * 1024) {
      toast.error("Контекст длиннее 128 КБ. Перенесите границу ближе к последним сообщениям.");
      return;
    }
    await act("run", () => sessionClient.generateDraft({ sessionId, selectedMessageIds: contextIds }));
  }

  return <div className="flex h-full min-h-0 flex-col bg-background">
    <header className="shrink-0 border-b px-4 py-2.5 sm:px-5">
      <div className="mx-auto flex max-w-[720px] items-center gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-accent text-[13px] font-medium">{person[0]?.toUpperCase()}</span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{person}</div>
          <div className="truncate text-xs text-muted-foreground">Telegram Business{bot?.username ? ` · через @${bot.username}` : ""}</div>
        </div>
        {bot && (replyOpen ? <span className="hidden items-center gap-1.5 rounded-full bg-secondary px-2.5 py-1 text-xs text-muted-foreground sm:inline-flex"><Clock3 className="size-3.5" />Ответить можно до {new Date(replyExpiresAt).toLocaleString("ru-RU", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</span>
          : <span className="inline-flex items-center gap-1 rounded-full bg-warning/10 px-2.5 py-1 text-xs text-warning"><LockKeyhole className="size-3.5" />Только чтение</span>)}
      </div>
    </header>

    <main ref={scroller} className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-5" onScroll={(event) => {
      const element = event.currentTarget;
      atBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 100;
    }}>
      <div className="mx-auto flex min-h-full max-w-[720px] flex-col justify-end gap-1.5">
        {loadError && <div role="alert" className="mb-3 rounded-lg border border-destructive/40 p-3 text-sm text-destructive">Не удалось обновить переписку. Уже загруженные сообщения доступны.</div>}
        {earlierCount > 0 && <Button variant="ghost" className="mb-3 self-center text-muted-foreground" onClick={() => setVisibleCount((count) => count + 50)}><ChevronUp className="size-4" /> Ранее ({earlierCount})</Button>}
        {shown.map((message, index) => {
          const absoluteIndex = messages.indexOf(message);
          const previous = shown[index - 1];
          const date = new Date(Number(message.createdAt) * 1000);
          const dateKey = date.toDateString();
          const newDay = !previous || new Date(Number(previous.createdAt) * 1000).toDateString() !== dateKey;
          const isNote = message.author === "owner" && message.source === "brigade" && message.delivery !== "sent";
          const outgoing = message.author !== "contact" && !isNote;
          const isTarget = target?.id === message.id;
          return <div key={message.id}>
            {newDay && <div className="my-4 text-center"><span className="rounded-full bg-secondary px-2.5 py-1 text-[11px] text-muted-foreground">{dateKey === today ? "Сегодня" : date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" })}</span></div>}
            {absoluteIndex === Math.max(0, boundary) && <div className="my-3 flex items-center gap-2 text-primary"><span className="h-px flex-1 bg-primary/45" /><span className="flex items-center gap-1 rounded-full border border-primary/45 bg-primary/10 px-2 py-1 text-[11px]"><GripHorizontal className="size-3" />Агент учитывает отсюда · {selectedCount} {plural(selectedCount, ["сообщение", "сообщения", "сообщений"])}{noteCount ? ` и ${noteCount} ${plural(noteCount, ["заметка", "заметки", "заметок"])}` : ""}</span><span className="h-px flex-1 bg-primary/45" /></div>}
            <div className={cn("group relative flex items-center gap-2", isNote ? "justify-center" : outgoing ? "justify-end" : "justify-start", absoluteIndex < boundary && "opacity-45")}>
              {!isNote && !outgoing && <div className="flex shrink-0 gap-0.5 opacity-100 transition-opacity sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100">
                <button type="button" title="Ответить на это сообщение" aria-label="Ответить на это сообщение" className="rounded-md p-1.5 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" onClick={() => { setTargetId(message.id); setMode("reply"); }}><Reply className="size-3.5" /></button>
                <button type="button" title="Контекст отсюда" aria-label="Контекст отсюда" className="rounded-md p-1.5 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setBoundaryId(message.id)}><ArrowDownToLine className="size-3.5" /></button>
              </div>}
              <div className={cn("max-w-[78%] rounded-[16px] border px-3.5 pt-2.5 pb-2 text-sm leading-[1.55] wrap-break-word", isNote ? "w-4/5 max-w-full rounded-xl border-dashed border-warning/40 bg-warning/5" : outgoing ? "rounded-br-[4px] bg-accent" : "rounded-bl-[4px] bg-card", isTarget && "border-primary/60", message.delivery === "uncertain" && "border-warning/50")}>
                {isNote && <div className="mb-1 flex items-center gap-1 text-[11px] text-warning"><StickyNote className="size-3" />Заметка · видят только вы и агент</div>}
                <span className="whitespace-pre-wrap">{message.content}</span>
                <time className="ml-2 text-[11px] text-muted-foreground" dateTime={date.toISOString()}>{date.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}</time>
              </div>
              {(isNote || outgoing) && <button type="button" title="Контекст отсюда" aria-label="Контекст отсюда" className="shrink-0 rounded-md p-1.5 opacity-100 transition-opacity hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring sm:opacity-0 sm:group-hover:opacity-100" onClick={() => setBoundaryId(message.id)}><ArrowDownToLine className="size-3.5" /></button>}
            </div>
            {isTarget && <div className="mt-0.5 flex items-center gap-1 text-[11px] text-primary"><CornerDownRight className="size-3" />Ответ на это сообщение</div>}
            {outgoing && message.delivery === "sent" && <div className="mt-0.5 text-right text-[11px] text-muted-foreground">✓ Отправлено</div>}
            {outgoing && message.delivery === "uncertain" && <div className="mt-0.5 text-right text-[11px] text-warning">⚠ Доставка не подтверждена</div>}
          </div>;
        })}
        {pending && <div className="flex justify-end"><div className="max-w-[78%] rounded-[16px_16px_4px_16px] border bg-accent px-3.5 py-2.5 text-sm opacity-60"><span className="whitespace-pre-wrap">{pending.text}</span><div className="mt-1 text-right text-[11px]">Ожидает отправки…</div></div></div>}
      </div>
    </main>

    <footer className="max-h-[min(65dvh,600px)] shrink-0 overflow-y-auto border-t bg-background px-4 pt-2 pb-5 sm:px-5">
      <div className="mx-auto flex max-w-[720px] flex-col gap-2.5">
        {run?.status === "running" && <div className="rounded-2xl border border-primary/40 bg-card p-3" role="status"><div className="flex items-center gap-2 text-[13px] font-semibold"><Sparkles className="size-3.5 text-primary" />Агент готовит черновик<Button size="sm" variant="outline" className="ml-auto" disabled={busy !== ""} onClick={() => void act("stop", () => sessionClient.cancelDraft({ sessionId }))}>{busy === "stop" && <Loader2 className="size-3.5 animate-spin" />}Остановить</Button></div><div className="mt-3 space-y-2" aria-hidden="true"><div className="h-2.5 w-full animate-pulse rounded-full bg-input" /><div className="h-2.5 w-[92%] animate-pulse rounded-full bg-input" /><div className="h-2.5 w-3/5 animate-pulse rounded-full bg-input" /></div><p className="mt-3 flex items-center gap-1.5 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />Агент читает {selected.length} сообщений…</p></div>}
        {run?.status === "failed" && <div className="rounded-lg border border-destructive/40 p-3 text-sm text-destructive" role="alert">Не удалось подготовить черновик: {run.error}</div>}
        {pending ? <div className="overflow-hidden rounded-xl border bg-card"><div className="flex flex-wrap items-center gap-2 p-3"><span className="text-[13px] font-medium">{busy === "send" ? "Отправляем…" : `Отправим через ${pending.seconds} с`}</span><span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">ответом на «{pendingTargetText}»</span>{sendDelay > 0 && <Button size="sm" variant="outline" disabled={busy === "send" || pending.seconds === 0 || sending.current} onClick={() => { setPending(null); if (!pending.draftId) setText(pending.text); }}><Undo2 className="size-3.5" />Отменить</Button>}</div>{sendDelay > 0 && <div className="h-0.5 bg-primary transition-[width] duration-1000 ease-linear" style={{ width: `${pending.seconds / sendDelay * 100}%` }} />}</div>
          : draft && (mode !== "reply" || !text.trim()) && <section className="rounded-2xl border border-primary/40 bg-card p-3" aria-label={draft.author === "owner" ? "Ваш черновик" : "Черновик агента"}>
            <div className="flex items-center gap-2 text-[13px] font-semibold"><Sparkles className="size-3.5 text-primary" />{draft.author === "owner" ? "Ваш черновик" : "Черновик агента"}<span className="text-xs font-normal text-muted-foreground">· учтено {selected.length} {plural(selected.length, ["сообщение", "сообщения", "сообщений"])}</span><button type="button" className="ml-auto rounded-md p-1 hover:bg-accent" aria-label="Скрыть черновик" onClick={() => setHiddenDraftIds((current) => new Set(current).add(draft.id))}><X className="size-4" /></button></div>
            <div className="mt-2 truncate rounded-lg bg-background/60 px-2.5 py-1.5 text-xs text-muted-foreground"><Reply className="mr-1 inline size-3" />Ответ на «{contacts.find((item) => item.id === draft.replyToId)?.content.slice(0, 80) ?? targetText}»</div>
            <p className="mt-2 whitespace-pre-wrap text-sm leading-relaxed">{draft.content}</p>
            <div className="mt-3 flex flex-wrap items-center gap-2">
              {replyOpen ? <Button size="sm" disabled={busy !== "" || [...draft.content].length > 4096} onClick={() => queueSend(draft.content, draft.id, draft.replyToId || target?.id)}><Send className="size-3.5" />Отправить</Button> : <Button size="sm" variant="outline" onClick={() => void navigator.clipboard.writeText(draft.content).then(() => toast.success("Текст скопирован"))}><Copy className="size-3.5" />Скопировать</Button>}
              <Button size="sm" variant="outline" onClick={() => { setText(draft.content); setEditingDraftId(draft.id); setTargetId(draft.replyToId); setMode("reply"); }}><Pencil className="size-3.5" />Изменить</Button>
              <Button size="sm" variant="ghost" disabled={busy !== "" || run?.status === "running"} onClick={() => void generate()}><Sparkles className="size-3.5" />Другой вариант</Button>
            </div>
            {[...draft.content].length > 4096 && <p className="mt-2 text-xs text-warning">Ответ длиннее 4096 символов. Сократите его перед отправкой.</p>}
          </section>}
        {draft && text.trim() && mode === "reply" && !pending && <div className="flex items-center gap-2 rounded-[10px] border bg-card px-3 py-1.5 text-xs text-muted-foreground"><Sparkles className="size-3.5 text-primary" />Черновик свёрнут — вы пишете сами<Button variant="ghost" size="sm" className="ml-auto" onClick={() => { setText(""); setEditingDraftId(""); }}>Вернуть черновик</Button></div>}
        {latestOutgoing?.delivery === "uncertain" && <div className="flex items-start gap-2 rounded-xl border border-warning/40 bg-warning/5 p-3 text-xs text-warning" role="alert"><TriangleAlert className="size-4 shrink-0" /><div><p className="font-medium">Telegram не подтвердил доставку</p><p className="mt-1 text-muted-foreground">Проверьте чат в Telegram перед новой отправкой. Этот ответ повторно не отправится.</p></div></div>}
        {bot === null && <p className="text-xs text-warning" role="alert">Не удалось проверить права Business-бота. Обновите страницу, чтобы повторить.</p>}
        {!replyOpen && bot && <div className="flex items-center gap-2 text-xs text-warning"><LockKeyhole className="size-3.5" />{canReply ? "24-часовое окно ответа истекло. Дождитесь нового входящего сообщения." : <>Бот только читает эту переписку. <Link to="/settings/telegram" className="underline underline-offset-2">Разрешить ответы</Link></>}</div>}
        {target && mode === "reply" && <div className="flex items-center gap-1.5 px-1 text-xs text-muted-foreground"><Reply className="size-3" />Ваш ответ · цитатой на «{targetText}»</div>}
        <div className={cn("rounded-[24px] border border-input bg-card/60 p-2", mode === "note" && "border-dashed border-warning/40 bg-warning/5")}>
          <Textarea aria-label={mode === "note" ? "Заметка" : "Ответ"} value={text} onChange={(event) => setText(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void submitComposer(); } }} placeholder={mode === "note" ? "Заметка для себя и агента — собеседник её не увидит" : replyOpen ? `Ответить ${person} самому…` : "Ответить через бота сейчас нельзя"} disabled={mode === "reply" && !replyOpen} className="min-h-12 max-h-36 resize-y border-0 bg-transparent px-2 text-[15px] shadow-none focus-visible:ring-0" />
          <div className="flex items-center justify-between gap-2">
            <div className="inline-flex rounded-full bg-secondary p-0.5 text-xs"><button type="button" aria-pressed={mode === "reply"} disabled={!replyOpen} onClick={() => { setText(""); setMode("reply"); }} className={cn("rounded-full px-3 py-1.5 disabled:opacity-40", mode === "reply" && "bg-accent")}>Ответ</button><button type="button" aria-pressed={mode === "note"} onClick={() => { setText(""); setMode("note"); }} className={cn("rounded-full px-3 py-1.5", mode === "note" && "bg-accent")}>Заметка</button></div>
            <div className="flex items-center gap-1">{mode === "reply" && <Button size="sm" variant="ghost" disabled={busy !== "" || run?.status === "running" || !contextIds.length} onClick={() => void generate()}><Sparkles className="size-3.5" />Черновик</Button>}<button type="button" aria-label={mode === "note" ? "Добавить заметку" : "Отправить ответ"} disabled={!text.trim() || busy !== "" || !!pending || (mode === "reply" && (!replyOpen || [...text].length > 4096))} onClick={() => void submitComposer()} className="flex size-8 items-center justify-center rounded-full bg-primary text-primary-foreground disabled:opacity-50"><ArrowUp className="size-4" /></button></div>
          </div>
        </div>
        {mode === "reply" && [...text].length > 4096 && <p className="text-xs text-warning">Ответ длиннее 4096 символов. Сократите его перед отправкой.</p>}
        {contextBytes > 112 * 1024 && <p className="text-xs text-warning">Контекст занимает около {Math.ceil(contextBytes / 1024)} КБ. Лимит — 128 КБ; перенесите границу ближе.</p>}
      </div>
    </footer>
  </div>;
}
