import { useEffect, useRef, useState } from "react";
import { Command } from "cmdk";
import { Dialog } from "radix-ui";
import { Search } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { sessionListTitle } from "./session-list";

export function SessionPalette({ sessions, state, onSelect, onCreate }: {
  sessions: Session[];
  state: "loading" | "ready" | "error";
  onSelect: (session: Session) => void;
  onCreate?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const previousFocus = useRef<HTMLElement | null>(null);
  const shortcut = /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘K" : "Ctrl+K";

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || event.isComposing || event.altKey || event.shiftKey) return;
      if (!(event.metaKey || event.ctrlKey) || event.code !== "KeyK" && event.key.toLowerCase() !== "k") return;
      event.preventDefault();
      if (!open) previousFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      setOpen((current) => !current);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open]);

  return (
    <Dialog.Root open={open} onOpenChange={setOpen}>
      <Dialog.Trigger asChild>
        <button type="button" aria-label="Поиск сессий" aria-keyshortcuts="Meta+K Control+K" title={`Поиск сессий · ${shortcut}`}
          onClick={(event) => { previousFocus.current = event.currentTarget; }}
          className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <Search aria-hidden className="size-3.5" />
        </button>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/40" />
        <Dialog.Content
          onCloseAutoFocus={(event) => {
            if (previousFocus.current?.isConnected) {
              event.preventDefault();
              previousFocus.current.focus({ preventScroll: true });
            }
          }}
          className="fixed left-1/2 top-[min(18vh,8rem)] z-50 w-[calc(100%-2rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-xl outline-none">
          <Dialog.Title className="sr-only">Перейти к сессии</Dialog.Title>
          <Dialog.Description className="sr-only">Поиск по названию и последнему сообщению. Стрелки для выбора, Enter для перехода.</Dialog.Description>
          <Command label="Поиск по названию и последнему сообщению" loop vimBindings={false}>
            <div className="flex items-center gap-3 border-b px-4">
              <Search aria-hidden className="size-4 shrink-0 text-muted-foreground" />
              <Command.Input autoFocus placeholder="Найти сессию…" aria-label="Поиск по названию и последнему сообщению"
                className="h-12 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground" />
              <Dialog.Close className="rounded border px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground" aria-label="Закрыть поиск">Esc</Dialog.Close>
            </div>
            <Command.List label="Сессии" className="max-h-[min(55dvh,24rem)] overflow-y-auto overscroll-contain scroll-py-2 p-2">
              {onCreate && <Command.Item value="create-session" keywords={["Новая сессия", "new session"]}
                onSelect={() => { setOpen(false); onCreate(); }}
                className="cursor-pointer rounded-lg px-3 py-2 text-sm outline-none data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground">Новая сессия</Command.Item>}
              {state === "loading" && <Command.Loading className="px-3 py-6 text-center text-sm text-muted-foreground">Загрузка сессий…</Command.Loading>}
              {state === "error" && <div role="alert" className="px-3 py-6 text-sm text-destructive">Не удалось загрузить сессии.</div>}
              {state === "ready" && <Command.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">Сессии не найдены.</Command.Empty>}
              {sessions.map((session) => (
                <Command.Item key={session.id} value={session.id} keywords={[sessionListTitle(session), session.lastMessage, session.groupLabel]}
                  onSelect={() => { setOpen(false); onSelect(session); }}
                  className="cursor-pointer rounded-lg px-3 py-2 text-sm outline-none data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground">
                  <div className="truncate font-medium">{sessionListTitle(session)}</div>
                  {session.lastMessage && <div className="truncate text-xs text-muted-foreground">{session.lastMessage}</div>}
                  {session.groupLabel && <div className="truncate text-xs text-muted-foreground">{session.groupLabel}</div>}
                </Command.Item>
              ))}
            </Command.List>
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
