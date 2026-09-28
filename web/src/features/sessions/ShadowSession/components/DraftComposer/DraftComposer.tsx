import { useEffect, useRef, useState } from "react";
import { Check, Loader2, Send } from "lucide-react";
import { toast } from "sonner";
import { sessionClient, telegramClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

type Props = {
  message: AcpMessage;
  sessionId: string;
  replyTarget: AcpMessage | null;
  latestContact: AcpMessage | null;
  busy: string;
  act: (name: string, action: () => Promise<unknown>) => Promise<boolean>;
};

export function DraftComposer({ message, sessionId, replyTarget, latestContact, busy, act }: Props) {
  const [text, setText] = useState(message.content);
  const [confirming, setConfirming] = useState(false);
  const [target, setTarget] = useState<AcpMessage | null>(replyTarget);
  const confirmButton = useRef<HTMLButtonElement>(null);
  const changed = text.trim() !== message.content.trim();
  const targetPreview = target?.content.trim().replace(/\s+/g, " ").slice(0, 110);
  const replyWindowExpired = target !== null && Number(target.createdAt) > 0 && Date.now() / 1000 - Number(target.createdAt) >= 24 * 60 * 60;
  const overTelegramLimit = [...text].length > 4096;
  const canSend = Boolean(target && !replyWindowExpired && !overTelegramLimit && text.trim() && busy === "");

  useEffect(() => {
    if (confirming) confirmButton.current?.focus();
  }, [confirming]);

  async function send() {
    if (!target || !text.trim()) return;
    const sent = await act(message.id, async () => {
      if (changed) {
        await sessionClient.editDraft({ sessionId, messageId: message.id, content: text.trim() });
      }
      await telegramClient.sendDraft({ sessionId, messageId: message.id, replyToMessageId: target.id });
    });
    setConfirming(false);
    if (sent) toast.success("Ответ отправлен в Telegram");
  }

  return (
    <section className="rounded-xl border border-primary/40 bg-card p-3 sm:p-4" aria-label="Черновик ответа">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Черновик ответа</h2>
        <span className="text-xs text-muted-foreground">Пока не отправлен</span>
      </div>
      <Textarea
        aria-label="Текст ответа"
        value={text}
        onChange={(event) => { setText(event.target.value); setConfirming(false); }}
        className="min-h-28 max-h-56 resize-y bg-background text-base sm:text-sm"
      />
      {target ? (
        <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
          Ответ в Telegram на сообщение собеседника: <span className="text-foreground">«{targetPreview || "Вложение"}»</span>
        </p>
      ) : (
        <p className="mt-2 text-xs text-warning">Нет входящего Business-сообщения, на которое можно ответить. Черновик можно сохранить здесь.</p>
      )}
      {latestContact && target?.id !== latestContact.id && <button type="button" className="mt-1 text-xs text-primary underline underline-offset-2" onClick={() => { setTarget(latestContact); setConfirming(false); }}>
        Вместо этого ответить на последнее входящее
      </button>}
      {replyWindowExpired && <p className="mt-2 text-xs text-warning">24-часовое окно ответа истекло. Этот черновик можно сохранить, но отправить через бота нельзя.</p>}
      {overTelegramLimit && <p className="mt-2 text-xs text-warning">Ответ длиннее 4096 символов. Сократите его перед отправкой в Telegram.</p>}
      {confirming && target && (
        <div className="mt-3 rounded-lg border border-primary/40 bg-primary/5 p-3" role="status">
          <p className="text-sm">Отправить этот ответ собеседнику в Telegram?</p>
          <p className="mt-1 text-xs text-muted-foreground">Текст будет {changed ? "сохранён и " : ""}отправлен сразу. Отменить отправку в Brigade нельзя.</p>
        </div>
      )}
      <div className="mt-3 flex flex-wrap justify-end gap-2">
        <Button variant="outline" className="min-h-11 sm:min-h-9" disabled={!changed || !text.trim() || busy !== ""} onClick={() => void act(message.id, () => sessionClient.editDraft({ sessionId, messageId: message.id, content: text.trim() }))}>
          {busy === message.id ? <Loader2 className="size-4 animate-spin" /> : <Check className="size-4" />} Сохранить
        </Button>
        {confirming ? (
          <>
            <Button variant="outline" className="min-h-11 sm:min-h-9" disabled={busy !== ""} onClick={() => setConfirming(false)}>Отмена</Button>
            <Button ref={confirmButton} className="min-h-11 sm:min-h-9" disabled={!canSend} onClick={() => void send()}>
              {busy === message.id ? <Loader2 className="size-4 animate-spin" /> : <Send className="size-4" />} Да, отправить
            </Button>
          </>
        ) : (
          <Button className="min-h-11 sm:min-h-9" disabled={!canSend} onClick={() => setConfirming(true)}>
            <Send className="size-4" /> Отправить в Telegram
          </Button>
        )}
      </div>
    </section>
  );
}
