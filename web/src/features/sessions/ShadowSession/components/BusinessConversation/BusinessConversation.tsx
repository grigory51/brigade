import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDownToLine, ChevronDown, ChevronUp, Copy, CornerDownRight, GripHorizontal, Loader2, LockKeyhole, Pencil, Reply, Send, Sparkles, StickyNote, Trash2, TriangleAlert, Undo2, X } from "lucide-react";
import { toast } from "sonner";
import { sessionClient, telegramClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import type { AcpConfigOptionValue } from "@/api/gen/brigade/v1/acp_pb";
import type { TelegramBot } from "@/api/gen/brigade/v1/telegram_pb";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { BusinessPhoto } from "./BusinessPhoto";

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
  const [modelId, setModelId] = useState("");
  const [models, setModels] = useState<AcpConfigOptionValue[] | null>(null);
  const [defaultModelId, setDefaultModelId] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState("");
  const [boundaryId, setBoundaryId] = useState("");
  const [targetId, setTargetId] = useState("");
  const [manualReplyOpen, setManualReplyOpen] = useState(false);
  const [manualReply, setManualReply] = useState("");
  const [instruction, setInstruction] = useState("");
  const [conversationNote, setConversationNote] = useState("");
  const [editingConversationNote, setEditingConversationNote] = useState(false);
  const [editingDraftId, setEditingDraftId] = useState("");
  const [editingDraftText, setEditingDraftText] = useState("");
  const [editingNoteId, setEditingNoteId] = useState("");
  const [editingNoteText, setEditingNoteText] = useState("");
  const [confirmDeleteNoteId, setConfirmDeleteNoteId] = useState("");
  const [hiddenDraftIds, setHiddenDraftIds] = useState<ReadonlySet<string>>(new Set());
  const [shownPhotoIds, setShownPhotoIds] = useState<ReadonlySet<string>>(new Set());
  const [pending, setPending] = useState<PendingSend | null>(null);
  const [visibleCount, setVisibleCount] = useState(INITIAL_VISIBLE);
  const [now, setNow] = useState(Date.now());
  const scroller = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const sending = useRef(false);

  const conversationNoteMessage = messages.find((message) => message.source === "brigade:contact-note");
  const conversationMessages = useMemo(() => messages.filter((message) => message.source !== "brigade:contact-note"), [messages]);
  const botId = conversationMessages.find((message) => message.source.startsWith("telegram-business/"))?.source.split("/")[1] ?? "";
  useEffect(() => {
    let active = true;
    void telegramClient.listBots({}).then((result) => { if (active) setBot(result.bots.find((item) => item.id === botId) ?? null); }).catch(() => { if (active) setBot(null); });
    void sessionClient.get({ sessionId }).then((result) => { if (active) { setSessionName(result.session?.name ?? ""); setModelId(result.session?.modelId ?? ""); } }).catch(() => {});
    return () => { active = false; };
  }, [botId, sessionId]);
  useEffect(() => {
    if (!editingConversationNote) setConversationNote(conversationNoteMessage?.content ?? "");
  }, [conversationNoteMessage?.content, editingConversationNote]);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);

  const contacts = useMemo(() => conversationMessages.filter((message) => message.author === "contact" && message.delivery !== "deleted" && message.source.startsWith("telegram-business/")), [conversationMessages]);
  const lastBusinessContact = [...conversationMessages].reverse().find((message) => message.author === "contact" && message.source.startsWith("telegram-business/"));
  const latestContact = lastBusinessContact?.delivery === "deleted" ? null : contacts.at(-1) ?? null;
  const target = contacts.find((message) => message.id === targetId) ?? latestContact;
  const lastSentIndex = conversationMessages.reduce((index, message, position) => message.author === "owner" && message.delivery === "sent" ? position : index, -1);
  const today = new Date(now).toDateString();
  const firstTodayIndex = conversationMessages.findIndex((message) => new Date(Number(message.createdAt) * 1000).toDateString() === today);
  const defaultBoundary = conversationMessages[Math.min(conversationMessages.length - 1, lastSentIndex >= 0 ? lastSentIndex + 1 : Math.max(0, firstTodayIndex))]?.id ?? "";
  const boundary = conversationMessages.findIndex((message) => message.id === (boundaryId || defaultBoundary));
  const selected = useMemo(() => conversationMessages.slice(Math.max(0, boundary)).filter((message) => !["draft", "stale", "sending", "failed", "uncertain", "deleted"].includes(message.delivery)), [conversationMessages, boundary]);
  const contextMessages = useMemo(() => [...new Map([...selected, ...(target ? [target] : [])].map((message) => [message.id, message])).values()], [selected, target]);
  const contextIds = useMemo(() => contextMessages.map((message) => message.id), [contextMessages]);
  const selectedCount = contextMessages.filter((message) => message.author !== "owner" || message.source !== "brigade").length;
  const noteCount = contextMessages.length - selectedCount;
  const contextBytes = useMemo(() => new TextEncoder().encode(JSON.stringify(contextMessages)).length, [contextMessages]);
  const drafts = conversationMessages.filter((message) => message.delivery === "draft" && !hiddenDraftIds.has(message.id));
  const draft = drafts.at(-1) ?? null;
  const latestOutgoing = [...conversationMessages].reverse().find((message) => message.author === "owner" && message.delivery !== "draft");
  const shown = useMemo(() => conversationMessages.slice(-visibleCount).filter((message) => message.delivery !== "draft" && message.delivery !== "stale"), [conversationMessages, visibleCount]);
  const earlierCount = Math.max(0, conversationMessages.length - visibleCount);
  const canReply = bot?.businessCanReply === true;
  const sendDelay = bot?.sendDelaySeconds ?? 5;
  const replyExpiresAt = latestContact ? Number(latestContact.createdAt) * 1000 + 24 * 60 * 60 * 1000 : 0;
  const replyOpen = canReply && replyExpiresAt > now;

  const person = sessionName.replace(/^Telegram\s*·\s*/, "") || "собеседнику";
  const targetText = target?.content.replace(/\s+/g, " ").slice(0, 60) || "сообщение";
  const pendingTargetText = contacts.find((message) => message.id === pending?.targetId)?.content.replace(/\s+/g, " ").slice(0, 60) || targetText;

  useEffect(() => {
    const element = scroller.current;
    if (element && atBottom.current) element.scrollTop = element.scrollHeight;
  }, [conversationMessages.length, run?.status, pending?.seconds]);

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
        const original = conversationMessages.find((message) => message.id === draftId);
        if (original?.content !== delivery.text) await sessionClient.editDraft({ sessionId, messageId: draftId, content: delivery.text });
      } else {
        const created = await sessionClient.createDraft({ sessionId, content: delivery.text, replyToMessageId: delivery.targetId });
        draftId = created.messageId;
      }
      await telegramClient.sendDraft({ sessionId, messageId: draftId, replyToMessageId: delivery.targetId });
    }).then((sent) => {
      if (sent) {
        setManualReply("");
        setManualReplyOpen(false);
        toast.success("Ответ отправлен в Telegram");
      } else if (!delivery.draftId) {
        setManualReply(delivery.text);
      }
      setPending(null);
      sending.current = false;
    });
  }, [act, conversationMessages, pending, sessionId]);

  function queueSend(value: string, draftId: string | null, replyToId = target?.id) {
    if (!replyToId || !replyOpen || !value.trim() || [...value].length > 4096 || pending) return;
    setPending({ draftId, text: value.trim(), targetId: replyToId, seconds: sendDelay });
    atBottom.current = true;
  }

  async function generate() {
    if (!target) {
      toast.error("Нет входящего сообщения для ответа");
      return;
    }
    if (contextBytes > 128 * 1024) {
      toast.error("Контекст длиннее 128 КБ. Перенесите границу ближе к последним сообщениям.");
      return;
    }
    await act("run", () => sessionClient.generateDraft({ sessionId, instruction: instruction.trim(), replyToMessageId: target.id, selectedMessageIds: contextIds }));
  }

  async function saveConversationNote() {
    const saved = await act("conversation-note", () => sessionClient.setConversationNote({ sessionId, content: conversationNote.trim() }));
    if (saved) {
      setEditingConversationNote(false);
      toast.success(conversationNote.trim() ? "Заметка о собеседнике сохранена" : "Заметка о собеседнике удалена");
    }
  }

  async function loadModels() {
    if (modelsLoading) return;
    setModelsLoading(true);
    setModelsError("");
    try {
      const result = await sessionClient.getModels({ sessionId });
      setModels(result.models);
      setDefaultModelId(result.defaultModelId);
    } catch (error) {
      setModelsError(error instanceof Error ? error.message : "Не удалось получить модели");
    } finally {
      setModelsLoading(false);
    }
  }

  return <div className="@container/business h-full min-h-0 min-w-0 bg-background">
    <div className="grid h-full min-h-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)_fit-content(44%)] @min-[48rem]/business:grid-cols-[minmax(0,1fr)_clamp(20rem,38%,30rem)] @min-[48rem]/business:grid-rows-[auto_minmax(0,1fr)]">
    <header className="shrink-0 border-b px-4 py-2.5 sm:px-5">
      <div className="mx-auto flex max-w-[720px] items-center gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-accent text-[13px] font-medium">{person[0]?.toUpperCase()}</span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{person}</div>
          <div className="truncate text-xs text-muted-foreground">Telegram Business{bot?.username ? ` · через @${bot.username}` : ""}</div>
        </div>
        {bot && !replyOpen && <span className="inline-flex items-center gap-1 rounded-full bg-warning/10 px-2.5 py-1 text-xs text-warning"><LockKeyhole className="size-3.5" />Только чтение</span>}
      </div>
    </header>

    <main ref={scroller} aria-label="Переписка" className="min-h-0 min-w-0 overflow-y-auto overscroll-contain px-4 py-5 sm:px-5 @min-[48rem]/business:col-start-1 @min-[48rem]/business:row-start-2" onScroll={(event) => {
      const element = event.currentTarget;
      atBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 100;
    }}>
      <div className="mx-auto flex min-h-full max-w-[720px] flex-col justify-end gap-1.5">
        {loadError && <div role="alert" className="mb-3 rounded-lg border border-destructive/40 p-3 text-sm text-destructive">Не удалось обновить переписку. Уже загруженные сообщения доступны.</div>}
        {earlierCount > 0 && <Button variant="ghost" className="mb-3 self-center text-muted-foreground" onClick={() => setVisibleCount((count) => count + 50)}><ChevronUp className="size-4" /> Ранее ({earlierCount})</Button>}
        {shown.map((message, index) => {
          const absoluteIndex = conversationMessages.indexOf(message);
          const previous = shown[index - 1];
          const date = new Date(Number(message.createdAt) * 1000);
          const dateKey = date.toDateString();
          const newDay = !previous || new Date(Number(previous.createdAt) * 1000).toDateString() !== dateKey;
          const isNote = message.author === "owner" && message.source === "brigade" && message.delivery === "received";
          const outgoing = message.author !== "contact" && !isNote;
          const isTarget = target?.id === message.id;
          const messageContent = message.hasTelegramBusinessPhoto ? message.content.replace(/^\[Вложение Telegram: telegram-[^\]]+-photo\.(?:jpe?g|png|webp)\]\s*/i, "") : message.content;
          return <div key={message.id}>
            {newDay && <div className="my-4 text-center"><span className="rounded-full bg-secondary px-2.5 py-1 text-[11px] text-muted-foreground">{dateKey === today ? "Сегодня" : date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" })}</span></div>}
            {absoluteIndex === Math.max(0, boundary) && <div className="my-3 flex items-center gap-2 text-primary"><span className="h-px flex-1 bg-primary/45" /><span className="flex items-center gap-1 rounded-full border border-primary/45 bg-primary/10 px-2 py-1 text-[11px]"><GripHorizontal className="size-3" />Агент учитывает отсюда · {selectedCount} {plural(selectedCount, ["сообщение", "сообщения", "сообщений"])}{noteCount ? ` и ${noteCount} ${plural(noteCount, ["заметка", "заметки", "заметок"])}` : ""}</span><span className="h-px flex-1 bg-primary/45" /></div>}
            <div className={cn("group relative flex items-center gap-2", isNote ? "justify-center" : outgoing ? "justify-end" : "justify-start", absoluteIndex < boundary && "opacity-45")}>
              {!isNote && !outgoing && message.delivery !== "deleted" && <div className="flex shrink-0 gap-0.5 opacity-100 transition-opacity sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100">
                <button type="button" title="Ответить на это сообщение" aria-label="Ответить на это сообщение" className="rounded-md p-1.5 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setTargetId(message.id)}><Reply className="size-3.5" /></button>
                <button type="button" title="Контекст отсюда" aria-label="Контекст отсюда" className="rounded-md p-1.5 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setBoundaryId(message.id)}><ArrowDownToLine className="size-3.5" /></button>
              </div>}
              <div className={cn("max-w-[78%] rounded-[16px] border px-3.5 pt-2.5 pb-2 text-sm leading-[1.55] wrap-break-word", isNote ? "w-4/5 max-w-full rounded-xl border-dashed border-warning/40 bg-warning/5" : outgoing ? "rounded-br-[4px] bg-accent" : "rounded-bl-[4px] bg-card", isTarget && "border-primary/60", message.delivery === "uncertain" && "border-warning/50")}>
                {isNote && <div className="mb-1 flex items-center gap-1 text-[11px] text-warning"><StickyNote className="size-3" />Заметка · видят только вы и агент
                  <span className="flex-1" />
                  <button type="button" title="Редактировать заметку" aria-label="Редактировать заметку" disabled={busy !== ""} className="rounded p-1 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50" onClick={() => { setEditingNoteId(message.id); setEditingNoteText(message.content); setConfirmDeleteNoteId(""); }}><Pencil className="size-3.5" /></button>
                  <button type="button" title="Удалить заметку" aria-label="Удалить заметку" disabled={busy !== ""} className="rounded p-1 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50" onClick={() => { setConfirmDeleteNoteId(message.id); setEditingNoteId(""); }}><Trash2 className="size-3.5" /></button>
                </div>}
                {editingNoteId === message.id ? <div className="space-y-2"><Textarea aria-label="Текст заметки" value={editingNoteText} onChange={(event) => setEditingNoteText(event.target.value)} className="min-h-24 resize-y bg-background" /><div className="flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={() => setEditingNoteId("")}>Отмена</Button><Button size="sm" disabled={!editingNoteText.trim() || busy !== ""} onClick={() => void act("edit-note", () => sessionClient.editMessage({ sessionId, messageId: message.id, content: editingNoteText.trim() })).then((saved) => { if (saved) setEditingNoteId(""); })}>{busy === "edit-note" && <Loader2 className="size-3.5 animate-spin" />}Сохранить</Button></div></div>
                  : confirmDeleteNoteId === message.id ? <div className="flex flex-wrap items-center gap-2 text-xs"><span className="mr-auto">Удалить заметку?</span><Button size="sm" variant="ghost" onClick={() => setConfirmDeleteNoteId("")}>Отмена</Button><Button size="sm" variant="destructive" disabled={busy !== ""} onClick={() => void act("delete-note", () => sessionClient.deleteMessage({ sessionId, messageId: message.id })).then((deleted) => { if (deleted) { setConfirmDeleteNoteId(""); if (boundaryId === message.id) setBoundaryId(""); } })}>{busy === "delete-note" && <Loader2 className="size-3.5 animate-spin" />}Удалить</Button></div>
                    : <>{message.hasTelegramBusinessPhoto && (shownPhotoIds.has(message.id) ? <BusinessPhoto sessionId={sessionId} messageId={message.id} /> : <Button type="button" size="sm" variant="outline" className="mb-2" onClick={() => setShownPhotoIds((current) => new Set(current).add(message.id))}>Показать фото</Button>)}{messageContent && <span className="whitespace-pre-wrap">{messageContent}</span>}<time className="ml-2 text-[11px] text-muted-foreground" dateTime={date.toISOString()}>{date.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}</time></>}
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

    <aside aria-label="Подготовка ответа" className="min-h-0 min-w-0 max-h-none overflow-y-auto overscroll-contain border-t bg-background px-4 pt-4 pb-5 sm:px-5 @min-[48rem]/business:col-start-2 @min-[48rem]/business:row-span-2 @min-[48rem]/business:row-start-1 @min-[48rem]/business:border-t-0 @min-[48rem]/business:border-l">
      <div className="mx-auto flex max-w-[720px] flex-col gap-2.5">
        {run?.status === "running" && <div className="rounded-2xl border border-primary/40 bg-card p-3" role="status"><div className="flex items-center gap-2 text-[13px] font-semibold"><Sparkles className="size-3.5 text-primary" />Агент готовит черновик<Button size="sm" variant="outline" className="ml-auto" disabled={busy !== ""} onClick={() => void act("stop", () => sessionClient.cancelDraft({ sessionId }))}>{busy === "stop" && <Loader2 className="size-3.5 animate-spin" />}Остановить</Button></div><div className="mt-3 space-y-2" aria-hidden="true"><div className="h-2.5 w-full animate-pulse rounded-full bg-input" /><div className="h-2.5 w-[92%] animate-pulse rounded-full bg-input" /><div className="h-2.5 w-3/5 animate-pulse rounded-full bg-input" /></div><p className="mt-3 flex items-center gap-1.5 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />Агент читает {contextMessages.length} сообщений…</p></div>}
        {run?.status === "failed" && <div className="rounded-lg border border-destructive/40 p-3 text-sm text-destructive" role="alert">Не удалось подготовить черновик: {run.error}</div>}
        {pending ? <div className="overflow-hidden rounded-xl border bg-card"><div className="flex flex-wrap items-center gap-2 p-3"><span className="text-[13px] font-medium">{busy === "send" ? "Отправляем…" : `Отправим через ${pending.seconds} с`}</span><span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">ответом на «{pendingTargetText}»</span>{sendDelay > 0 && <Button size="sm" variant="outline" disabled={busy === "send" || pending.seconds === 0 || sending.current} onClick={() => { setPending(null); if (!pending.draftId) setManualReply(pending.text); }}><Undo2 className="size-3.5" />Отменить</Button>}</div>{sendDelay > 0 && <div className="h-0.5 bg-primary transition-[width] duration-1000 ease-linear" style={{ width: `${pending.seconds / sendDelay * 100}%` }} />}</div>
          : draft && <section className="rounded-2xl border border-primary/40 bg-card p-3" aria-label={draft.author === "owner" ? "Ваш черновик" : "Черновик агента"}>
            <div className="flex items-center gap-2 text-[13px] font-semibold"><Sparkles className="size-3.5 text-primary" />{draft.author === "owner" ? "Ваш черновик" : "Черновик агента"}<button type="button" className="ml-auto rounded-md p-1 hover:bg-accent" aria-label="Скрыть черновик" onClick={() => setHiddenDraftIds((current) => new Set(current).add(draft.id))}><X className="size-4" /></button></div>
            <div className="mt-2 truncate rounded-lg bg-background/60 px-2.5 py-1.5 text-xs text-muted-foreground"><Reply className="mr-1 inline size-3" />Ответ на «{contacts.find((item) => item.id === draft.replyToId)?.content.slice(0, 80) ?? targetText}»</div>
            {editingDraftId === draft.id ? <div className="mt-2 space-y-2"><Textarea aria-label="Текст черновика" value={editingDraftText} onChange={(event) => setEditingDraftText(event.target.value)} className="min-h-28 resize-y bg-background" /><div className="flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={() => { setEditingDraftId(""); setEditingDraftText(""); }}>Отмена</Button><Button size="sm" disabled={!editingDraftText.trim() || busy !== ""} onClick={() => void act("edit-draft", () => sessionClient.editDraft({ sessionId, messageId: draft.id, content: editingDraftText.trim() })).then((saved) => { if (saved) { setEditingDraftId(""); setEditingDraftText(""); toast.success("Черновик сохранён"); } })}>{busy === "edit-draft" && <Loader2 className="size-3.5 animate-spin" />}Сохранить</Button></div></div>
              : <><p className="mt-2 max-h-40 overflow-y-auto whitespace-pre-wrap text-sm leading-relaxed">{draft.content}</p><div className="mt-3 flex flex-wrap items-center gap-2">{replyOpen && <Button size="sm" disabled={busy !== "" || [...draft.content].length > 4096} onClick={() => queueSend(draft.content, draft.id, draft.replyToId || target?.id)}><Send className="size-3.5" />Отправить</Button>}{!replyOpen && <Button size="sm" variant="outline" onClick={() => void navigator.clipboard.writeText(draft.content).then(() => toast.success("Текст скопирован"))}><Copy className="size-3.5" />Скопировать</Button>}
              <Button size="sm" variant="outline" onClick={() => { setEditingDraftId(draft.id); setEditingDraftText(draft.content); }}><Pencil className="size-3.5" />Изменить</Button><Button size="sm" variant="ghost" disabled={busy !== "" || run?.status === "running" || !target} onClick={() => void generate()}><Sparkles className="size-3.5" />Другой вариант</Button></div>{[...draft.content].length > 4096 && <p className="mt-2 text-xs text-warning">Ответ длиннее 4096 символов. Сократите его перед отправкой.</p>}</>}</section>}
        {latestOutgoing?.delivery === "uncertain" && <div className="flex items-start gap-2 rounded-xl border border-warning/40 bg-warning/5 p-3 text-xs text-warning" role="alert"><TriangleAlert className="size-4 shrink-0" /><div><p className="font-medium">Telegram не подтвердил доставку</p><p className="mt-1 text-muted-foreground">Проверьте чат в Telegram перед новой отправкой. Этот ответ повторно не отправится.</p></div></div>}
        {bot === null && <p className="text-xs text-warning" role="alert">Не удалось проверить права Business-бота. Обновите страницу, чтобы повторить.</p>}
        {!replyOpen && bot && <div className="flex items-center gap-2 text-xs text-warning"><LockKeyhole className="size-3.5" />{canReply ? (draft ? "Через бота ответить уже нельзя. Скопируйте черновик и отправьте его в Telegram." : "Через бота ответить уже нельзя. Ответьте напрямую в Telegram.") : <>Бот только читает эту переписку. <Link to="/settings/telegram" className="underline underline-offset-2">Разрешить ответы</Link></>}</div>}
        <section className="rounded-lg border bg-card/60 px-3 py-2" aria-label="Заметка о собеседнике">
          <div className="flex items-center gap-2"><StickyNote className="size-3.5 text-warning" /><button type="button" className="min-w-0 flex-1 text-left text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setEditingConversationNote((editing) => !editing)}><span className="font-medium">Заметка о собеседнике</span>{conversationNote && !editingConversationNote && <span className="ml-2 text-muted-foreground">{conversationNote}</span>}</button><button type="button" aria-label={editingConversationNote ? "Свернуть заметку о собеседнике" : "Развернуть заметку о собеседнике"} className="rounded p-1 hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setEditingConversationNote((editing) => !editing)}><ChevronDown className={cn("size-3.5 transition-transform", editingConversationNote && "rotate-180")} /></button></div>
          {editingConversationNote && <div className="mt-3 space-y-2"><Textarea aria-label="Заметка о собеседнике" value={conversationNote} onChange={(event) => setConversationNote(event.target.value)} placeholder="Например: предпочитает короткие сообщения и общение на «вы»" className="min-h-20 resize-y" /><div className="flex justify-end gap-2"><Button size="sm" variant="ghost" disabled={busy === "conversation-note"} onClick={() => { setConversationNote(conversationNoteMessage?.content ?? ""); setEditingConversationNote(false); }}>Отмена</Button><Button size="sm" disabled={busy !== ""} onClick={() => void saveConversationNote()}>{busy === "conversation-note" && <Loader2 className="size-3.5 animate-spin" />}Сохранить</Button></div></div>}
        </section>
        <section className="rounded-[20px] border border-primary/35 bg-card p-3" aria-label="Инструкция для черновика">
          <div className="flex items-center gap-2"><Sparkles className="size-3.5 text-primary" /><label htmlFor="draft-instruction" className="text-sm font-medium">Инструкция для черновика</label></div>
          <Textarea id="draft-instruction" aria-label="Инструкция для черновика" value={instruction} onChange={(event) => setInstruction(event.target.value)} placeholder="Необязательно: цель, тон, что уточнить или предложить" className="mt-2 min-h-16 resize-y bg-background" />
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
            <DropdownMenu onOpenChange={(open) => { if (open && models === null && !modelsLoading) void loadModels(); }}>
              <DropdownMenuTrigger asChild><Button type="button" size="sm" variant="ghost" disabled={busy !== "" || run?.status === "running"} className="h-7 gap-1.5 px-2 text-xs font-normal text-muted-foreground"><span>Модель:</span><span className="max-w-36 truncate text-foreground">{models?.find((item) => item.value === modelId)?.name ?? (modelId || "По умолчанию")}</span><ChevronDown className="size-3" /></Button></DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="start" className="w-64">
                {modelsLoading && <div className="flex items-center gap-2 px-2 py-2 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />Загружаем модели агента…</div>}
                {modelsError && <DropdownMenuItem onSelect={() => void loadModels()} className="text-xs text-destructive">Не удалось загрузить. Повторить</DropdownMenuItem>}
                {models && <><DropdownMenuItem onSelect={() => void act("model", () => sessionClient.setModel({ sessionId, modelId: "" })).then((saved) => { if (saved) setModelId(""); })}>По умолчанию{defaultModelId ? ` · ${models.find((item) => item.value === defaultModelId)?.name ?? defaultModelId}` : ""}</DropdownMenuItem>{models.map((item) => <DropdownMenuItem key={item.value} onSelect={() => void act("model", () => sessionClient.setModel({ sessionId, modelId: item.value })).then((saved) => { if (saved) setModelId(item.value); })}>{item.name}{item.value === modelId && <span className="ml-auto text-primary">✓</span>}</DropdownMenuItem>)}{models.length === 0 && <div className="px-2 py-2 text-xs text-muted-foreground">Агент не сообщил список моделей ACP</div>}</>}
              </DropdownMenuContent>
            </DropdownMenu>
            <Button size="sm" className="ml-auto" disabled={busy !== "" || run?.status === "running" || !target} onClick={() => void generate()}><Sparkles className="size-3.5" />Сгенерировать</Button>
          </div>
        </section>
        {replyOpen && <div>{manualReplyOpen ? <section className="rounded-[20px] border border-input bg-card/60 p-2" aria-label="Ручной ответ"><div className="mb-1.5 flex items-center gap-1.5 px-1 text-xs text-muted-foreground"><Reply className="size-3 shrink-0" /><span className="truncate">Ответ цитатой на «{targetText}»</span></div><Textarea aria-label="Ручной ответ" value={manualReply} onChange={(event) => setManualReply(event.target.value)} placeholder={`Ответить ${person} самому…`} className="min-h-20 max-h-36 resize-y border-0 bg-transparent px-2 text-[15px] shadow-none focus-visible:ring-0" /><div className="mt-2 flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={() => { setManualReply(""); setManualReplyOpen(false); }}>Отмена</Button><Button size="sm" disabled={!manualReply.trim() || busy !== "" || !!pending || [...manualReply].length > 4096} onClick={() => queueSend(manualReply, null)}><Send className="size-3.5" />Отправить</Button></div></section> : <Button type="button" size="sm" variant="ghost" onClick={() => setManualReplyOpen(true)}><Reply className="size-3.5" />Написать вручную</Button>}</div>}
        {manualReplyOpen && [...manualReply].length > 4096 && <p className="text-xs text-warning">Ответ длиннее 4096 символов. Сократите его перед отправкой.</p>}
        {contextBytes > 112 * 1024 && <p className="text-xs text-warning">Контекст занимает около {Math.ceil(contextBytes / 1024)} КБ. Лимит — 128 КБ; перенесите границу ближе.</p>}
      </div>
    </aside>
    </div>
  </div>;
}
