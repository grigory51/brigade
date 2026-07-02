import { useEffect, useState } from "react";
import { ExternalLink } from "lucide-react";
import { sessionClient } from "@/api/client";
import type { Preview } from "@/api/gen/brigade/v1/session_pb";

/**
 * PreviewLinks — ссылки на dev-серверы, которые агент поднял и зарегистрировал в
 * сессии (brigade preview). Список опрашивается раз в 5 секунд, пока компонент
 * смонтирован; при пустом списке ничего не рендерится.
 */
export function PreviewLinks({ sessionId }: { sessionId: string }) {
  const [previews, setPreviews] = useState<Preview[]>([]);

  useEffect(() => {
    let stopped = false;

    const poll = async () => {
      try {
        const res = await sessionClient.listPreviews({ sessionId });
        if (!stopped) setPreviews(res.previews);
      } catch {
        // Сессия могла быть удалена/недоступна — не шумим, попробуем в следующем тике.
      }
    };

    void poll();
    const timer = setInterval(() => void poll(), 5000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [sessionId]);

  if (previews.length === 0) return null;

  return (
    <div className="flex shrink-0 items-center gap-1.5">
      {previews.map((p) => (
        <a
          key={p.port}
          href={p.url}
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-1 rounded-md border border-transparent bg-success/10 px-2 py-0.5 text-xs text-success transition-colors hover:bg-success/20"
        >
          <ExternalLink className="size-3" />
          {p.name || `:${p.port}`}
        </a>
      ))}
    </div>
  );
}
