import { useEffect, useRef, useState } from "react";
import { useAuiState } from "@assistant-ui/react";
import { ChevronDown, Loader2, MessageCircle } from "lucide-react";
import { ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { pluginClient, sessionClient } from "@/api/client";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { DockChip } from "@/features/acp/dock/DockChip";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

type PaneProps = { session: Session; name?: string; onChanged: (session: Session) => void };

export function SessionPaneControl(props: PaneProps) {
  const busy = useAuiState((state) => state.thread.isRunning || state.thread.isLoading);
  return <SessionPaneSelect {...props} disabled={busy} />;
}

export function SessionPaneSelect({ session, name, onChanged, disabled = false }: PaneProps & { disabled?: boolean }) {
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [loadState, setLoadState] = useState<"loading" | "ready" | "error">("loading");
  const [attempt, setAttempt] = useState(0);
  const [changing, setChanging] = useState(false);
  const request = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    pluginClient.list({}, { signal: controller.signal }).then((result) => {
      if (controller.signal.aborted) return;
      setPlugins(result.plugins.filter((plugin) => plugin.compatible && plugin.configured));
      setLoadState("ready");
    }).catch(() => {
      if (!controller.signal.aborted) setLoadState("error");
    });
    return () => { controller.abort(); };
  }, [attempt]);

  useEffect(() => () => { request.current?.abort(); }, [session.id]);

  const change = async (experienceId: string) => {
    if (disabled || request.current || experienceId === session.experienceId) return;
    const controller = new AbortController();
    request.current = controller;
    setChanging(true);
    try {
      const result = await sessionClient.setExperience({ sessionId: session.id, experienceId }, { signal: controller.signal });
      if (controller.signal.aborted) return;
      if (!result.session) throw new Error("Сервер не вернул обновлённую сессию");
      onChanged(result.session);
    } catch (error) {
      if (!controller.signal.aborted) toast.error(error instanceof ConnectError ? error.rawMessage : error instanceof Error ? error.message : "Не удалось переключить интерфейс");
    } finally {
      if (request.current === controller) {
        request.current = null;
        if (!controller.signal.aborted) setChanging(false);
      }
    }
  };

  const label = session.experienceId
    ? name || plugins.find((plugin) => plugin.id === session.experienceId)?.name || session.experienceId
    : "Без панели";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <DockChip
          aria-label={`Панель: ${label}`}
          title={`Панель: ${label}`}
          aria-busy={changing}
          disabled={changing}
          className="max-w-40"
        >
          {changing ? <Loader2 aria-hidden="true" className="size-3.5 shrink-0 motion-safe:animate-spin" /> : <MessageCircle aria-hidden="true" className="size-3.5 shrink-0" />}
          <span className="truncate">{label}</span>
          <ChevronDown aria-hidden="true" className="size-3.5 shrink-0" />
        </DockChip>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-w-[calc(100vw-2rem)] data-[state=open]:animate-none data-[state=closed]:animate-none">
        <DropdownMenuLabel>Панель сессии</DropdownMenuLabel>
        {disabled && <div role="status" className="max-w-64 px-2 py-1.5 text-xs text-muted-foreground">Переключение доступно после завершения работы агента.</div>}
        <DropdownMenuRadioGroup value={session.experienceId} onValueChange={(id) => { void change(id); }}>
          <DropdownMenuRadioItem value="" disabled={disabled || changing}>Без панели</DropdownMenuRadioItem>
          {plugins.map((plugin) => (
            <DropdownMenuRadioItem key={plugin.id} value={plugin.id} disabled={disabled || changing}>
              <span className="truncate">{plugin.name}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {loadState === "loading" && <div role="status" className="flex items-center gap-2 px-2 py-1.5 text-xs text-muted-foreground"><Loader2 aria-hidden="true" className="size-3.5 motion-safe:animate-spin" />Загрузка приложений…</div>}
        {loadState === "error" && <>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={(event) => { event.preventDefault(); setLoadState("loading"); setAttempt((value) => value + 1); }}>Не удалось загрузить приложения. Повторить</DropdownMenuItem>
        </>}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
