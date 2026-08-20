import { useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertCircle, AppWindow, Loader2, MessagesSquare } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { pluginClient } from "@/api/client";
import { AcpSession } from "@/features/acp/AcpPage";
import { useSessionHeader } from "@/features/sessions/SessionHeaderSlot";
import { cn } from "@/lib/utils";
import { McpAppFrame } from "./McpAppFrame";

export function PluginSession({ session }: { session: Session }) {
  const [view, setView] = useState<"app" | "chat">("app");
  const [name, setName] = useState("Приложение");
  const [entryTool, setEntryTool] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setEntryTool("");
    setError("");
    pluginClient.get({ sessionId: session.id }).then((plugin) => {
      if (!cancelled) {
        setName(plugin.name || "Приложение");
        setEntryTool(plugin.entryTool);
      }
    }).catch((reason) => {
      if (!cancelled) setError(reason instanceof Error ? reason.message : "Не удалось загрузить плагин");
    });
    return () => { cancelled = true; };
  }, [session.id, session.experienceId]);

  const tabs = useMemo(
    () => (
      <div className="flex items-center gap-1 rounded-lg bg-muted/60 p-1" role="tablist" aria-label="Режим сессии">
        <ViewTab id="plugin-app-tab" controls="plugin-app-panel" active={view === "app"} onClick={() => setView("app")}>
          <AppWindow className="size-3.5" />
          {name}
        </ViewTab>
        <ViewTab id="plugin-chat-tab" controls="plugin-chat-panel" active={view === "chat"} onClick={() => setView("chat")}>
          <MessagesSquare className="size-3.5" />
          Чат
        </ViewTab>
      </div>
    ),
    [name, view],
  );
  useSessionHeader({ title: tabs });

  return (
    <div className="relative h-full min-h-0 bg-background">
      <div id="plugin-app-panel" className="h-full min-h-0" role="tabpanel" aria-labelledby="plugin-app-tab" hidden={view !== "app"}>
        {error ? (
          <div className="flex h-full items-center justify-center gap-2 text-sm text-destructive">
            <AlertCircle className="size-4" />
            <span>{error}</span>
          </div>
        ) : entryTool ? (
          <McpAppFrame sessionId={session.id} entryTool={entryTool} />
        ) : (
          <div className="flex h-full items-center justify-center">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        )}
      </div>
      <div id="plugin-chat-panel" className="h-full min-h-0" role="tabpanel" aria-labelledby="plugin-chat-tab" hidden={view !== "chat"}>
        <AcpSession sessionId={session.id} />
      </div>
    </div>
  );
}

function ViewTab({
  active,
  controls,
  children,
  id,
  onClick,
}: {
  active: boolean;
  controls: string;
  children: ReactNode;
  id: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="tab"
      id={id}
      aria-controls={controls}
      aria-selected={active}
      onClick={onClick}
      className={cn(
        "flex h-7 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium transition-colors",
        active
          ? "bg-background text-foreground shadow-sm"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}
