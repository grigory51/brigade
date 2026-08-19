import { useEffect, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { pluginClient } from "@/api/client";
import { AcpSession } from "@/features/acp/AcpPage";
import { McpAppFrame } from "./McpAppFrame";

export function PluginSession({ session }: { session: Session }) {
  const [entryTool, setEntryTool] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setEntryTool("");
    setError("");
    pluginClient.get({ sessionId: session.id }).then((plugin) => {
      if (!cancelled) setEntryTool(plugin.entryTool);
    }).catch((reason) => {
      if (!cancelled) setError(reason instanceof Error ? reason.message : "Не удалось загрузить плагин");
    });
    return () => { cancelled = true; };
  }, [session.id, session.experienceId]);

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="min-h-0 flex-[3] border-b border-border bg-card/30">
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
      <div className="min-h-72 flex-[2]">
        <AcpSession sessionId={session.id} />
      </div>
    </div>
  );
}
