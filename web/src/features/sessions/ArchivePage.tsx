import { AssistantRuntimeProvider } from "@assistant-ui/react";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { Archive, ChevronLeft, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { archiveClient, sessionClient } from "@/api/client";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { cn } from "@/lib/utils";
import { AcpThread } from "@/features/acp/AcpThread";
import { useArchivedRuntime } from "@/features/acp/useArchivedRuntime";

// sessionTitle — подпись сессии для карточки: имя, либо производная (тип агента) для
// сессий без имени.
function sessionTitle(s: Session): string {
  return s.name.trim() || s.agentType || "Сессия";
}

function formatDate(unixSec: bigint): string {
  const d = new Date(Number(unixSec) * 1000);
  return d.toLocaleDateString("ru-RU", {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}

// ArchivePage — страница архива: карточки заархивированных сессий (title + summary от
// агента). Клик открывает readonly-чат.
export function ArchivePage() {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [deletingIds, setDeletingIds] = useState<Set<string>>(new Set());
  const navigate = useNavigate();

  async function onDelete(id: string) {
    setDeletingIds((prev) => new Set(prev).add(id));
    try {
      await sessionClient.delete({ sessionId: id });
      setSessions((prev) => (prev ?? []).filter((s) => s.id !== id));
      toast.success("Сессия удалена из архива");
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось удалить сессию",
      );
      setDeletingIds((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  }

  useEffect(() => {
    let alive = true;
    archiveClient
      .list({})
      .then((r) => {
        if (alive) setSessions(r.sessions);
      })
      .catch(() => {
        if (alive) setSessions([]);
      });
    return () => {
      alive = false;
    };
  }, []);

  if (sessions === null) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Loader2 className="size-5 animate-spin" />
      </div>
    );
  }

  return (
    <div className="mx-auto h-full w-full max-w-4xl overflow-y-auto px-6 py-8">
      <div className="mb-6 flex items-center gap-2">
        <Archive className="size-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">Архив</h1>
      </div>

      {sessions.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          Здесь появятся заархивированные сессии. Архивируйте сессию из её меню в списке
          слева.
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {sessions.map((s) => {
            const deleting = deletingIds.has(s.id);
            return (
              <div
                key={s.id}
                onClick={() => !deleting && navigate(`/archive/${s.id}`)}
                className={cn(
                  "group flex cursor-pointer flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-colors hover:border-foreground/20 hover:bg-accent/40",
                  deleting && "pointer-events-none opacity-50",
                )}
              >
                <div className="flex items-baseline justify-between gap-2">
                  <span className="min-w-0 truncate font-medium">
                    {sessionTitle(s)}
                  </span>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <span className="text-xs text-muted-foreground">
                      {formatDate(s.createdAt)}
                    </span>
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        void onDelete(s.id);
                      }}
                      title="Удалить из архива"
                      className="flex size-6 items-center justify-center rounded-md text-muted-foreground opacity-0 transition hover:bg-muted hover:text-foreground group-hover:opacity-100"
                    >
                      {deleting ? (
                        <Loader2 className="size-3.5 animate-spin" />
                      ) : (
                        <Trash2 className="size-3.5" />
                      )}
                    </button>
                  </div>
                </div>
                <p className="line-clamp-3 text-sm text-muted-foreground">
                  {s.summary || "Без описания."}
                </p>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// ArchiveSessionPage — readonly-просмотр заархивированной сессии: шапка с заголовком и
// summary + лента чата без поля ввода (из снимка истории).
export function ArchiveSessionPage() {
  const { sessionId = "" } = useParams();
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-2">
        <Link
          to="/archive"
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-4" />
          Архив
        </Link>
        <span className="ml-1 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          только чтение
        </span>
      </div>
      <div className="min-h-0 flex-1">
        <ArchivedAcpSession key={sessionId} sessionId={sessionId} />
      </div>
    </div>
  );
}

// ArchivedAcpSession — readonly ACP-лента архивной сессии: рантайм из снимка истории,
// composer скрыт (AcpThread readonly).
function ArchivedAcpSession({ sessionId }: { sessionId: string }) {
  const { runtime, a2ui } = useArchivedRuntime(sessionId);
  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <div className="h-full">
        <AcpThread
          commands={[]}
          plan={[]}
          a2ui={a2ui}
          configOptions={[]}
          onConfigChange={() => {}}
          readonly
        />
      </div>
    </AssistantRuntimeProvider>
  );
}
