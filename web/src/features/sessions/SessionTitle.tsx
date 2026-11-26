import { useRef, useState } from "react";
import { Check, Loader2, Pencil, X } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { useSessionShell } from "./SessionLayout";

export function SessionTitle({ session }: { session: Session }) {
  const { activeSession, renameSession, renamingIds } = useSessionShell();
  const current = activeSession?.id === session.id ? activeSession : session;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState(false);
  const inFlight = useRef(false);
  const title = current.name || "Новая сессия";
  const busy = pending || renamingIds.has(session.id);

  if (editing) return (
    <form className="flex max-w-md items-center gap-1" onSubmit={async (event) => {
      event.preventDefault();
      if (inFlight.current || busy) return;
      inFlight.current = true;
      setPending(true);
      try {
        if (await renameSession(session.id, draft.trim())) setEditing(false);
      } finally {
        inFlight.current = false;
        setPending(false);
      }
    }}>
      <input autoFocus aria-label="Название сессии" value={draft} disabled={busy}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => { if (event.key === "Escape" && !busy) setEditing(false); }}
        className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      />
      <button type="submit" aria-label="Сохранить название" disabled={busy} className="flex size-8 shrink-0 items-center justify-center rounded-md hover:bg-accent disabled:opacity-50">
        {pending ? <Loader2 className="size-4 motion-safe:animate-spin" /> : <Check className="size-4" />}
      </button>
      <button type="button" aria-label="Отменить переименование" disabled={busy} onClick={() => setEditing(false)} className="flex size-8 shrink-0 items-center justify-center rounded-md hover:bg-accent disabled:opacity-50"><X className="size-4" /></button>
    </form>
  );

  return (
    <button type="button" title="Переименовать сессию" aria-label={`Переименовать сессию: ${title}`} disabled={busy} aria-busy={busy}
      onClick={() => { setDraft(current.name); setEditing(true); }}
      className="group/title flex h-8 max-w-full items-center gap-2 rounded-md px-1 text-left text-sm font-medium hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      <span className="truncate">{title}</span>
      <Pencil aria-hidden="true" className="size-3 shrink-0 text-muted-foreground opacity-0 group-hover/title:opacity-100 group-focus-visible/title:opacity-100 [@media(hover:none)]:opacity-100" />
    </button>
  );
}
