import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { acpClient, sessionClient } from "@/api/client";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { Markdown } from "@/components/markdown";
import { BusinessConversation } from "./components/BusinessConversation/BusinessConversation";

type Run = { id: string; status: string; error: string };

export function ShadowSession({ sessionId }: { sessionId: string }) {
  const [messages, setMessages] = useState<AcpMessage[] | null>(null);
  const [run, setRun] = useState<Run | null>(null);
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

  if (!messages) {
    return <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground" role="status">
      {!loadError && <Loader2 className="size-4 animate-spin" />}
      {loadError ? "Не удалось загрузить переписку. Повторяем подключение…" : "Загружаем переписку…"}
    </div>;
  }
  if (messages.some((message) => message.source.startsWith("telegram-business/"))) {
    return <BusinessConversation sessionId={sessionId} messages={messages} run={run} busy={busy} act={act} loadError={loadError} />;
  }
  return <main className="h-full overflow-y-auto p-5">
    <div className="mx-auto max-w-3xl space-y-4">
      <p className="text-sm text-muted-foreground">Переписка без Telegram Business доступна только для чтения.</p>
      {messages.map((message) => <article key={message.id} className="rounded-xl border p-3">
        <p className="mb-1 text-xs text-muted-foreground">{message.author === "owner" ? "Вы" : message.author === "agent" ? "Агент" : "Собеседник"}</p>
        <Markdown className="wrap-break-word text-sm">{message.content}</Markdown>
      </article>)}
    </div>
  </main>;
}
