import { useEffect, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { pluginClient } from "@/api/client";
import { AcpSession } from "@/features/acp/AcpPage";
import { useSessionHeader } from "@/features/sessions/SessionHeaderSlot";
import { McpAppFrame } from "./McpAppFrame";

export function PluginSession({ session }: { session: Session }) {
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

  useSessionHeader({ title: name });

  return (
    <div className="grid h-full min-h-0 grid-rows-[minmax(0,1fr)_minmax(220px,36%)] bg-background">
      <section className="min-h-0">
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
      </section>
      <section className="min-h-0 border-t border-border/70 bg-background">
        <AcpSession sessionId={session.id} workspace />
      </section>
    </div>
  );
}
