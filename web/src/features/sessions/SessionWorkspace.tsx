import { Component, useEffect, useState, type ReactNode } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import { pluginClient } from "@/api/client";
import { AcpSession } from "@/features/acp/AcpPage";
import { SessionPaneControl } from "./SessionPaneControl";
import { SessionTitle } from "@/features/sessions/SessionTitle";
import { McpAppFrame } from "@/features/plugins/McpAppFrame";

class PaneBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return <div role="alert" className="flex h-full flex-col items-center justify-center gap-3 p-4 text-sm">
        <p className="text-destructive">Не удалось показать панель: {this.state.error.message}</p>
        <button type="button" className="underline underline-offset-4" onClick={() => this.setState({ error: null })}>Повторить</button>
      </div>;
    }
    return this.props.children;
  }
}

export function SessionWorkspace({ session }: { session: Session }) {
  const [currentSession, setCurrentSession] = useState(session);
  const [loaded, setLoaded] = useState<{ key: string; plugin?: Plugin; error?: string } | null>(null);
  const paneKey = `${currentSession.id}:${currentSession.experienceId}:${currentSession.experienceVersion}`;
  const plugin = loaded?.key === paneKey ? loaded.plugin : undefined;
  const error = loaded?.key === paneKey ? loaded.error || (plugin && !plugin.entryTool ? "Плагин не объявляет entry tool" : undefined) : undefined;

  useEffect(() => { setCurrentSession(session); }, [session]);

  useEffect(() => {
    if (!currentSession.experienceId) return;
    const controller = new AbortController();
    pluginClient.get({ sessionId: currentSession.id }, { signal: controller.signal }).then((plugin) => {
      if (!controller.signal.aborted) setLoaded({ key: paneKey, plugin });
    }).catch((reason) => {
      if (!controller.signal.aborted) setLoaded({
        key: paneKey,
        error: reason instanceof Error ? reason.message : "Не удалось загрузить плагин",
      });
    });
    return () => { controller.abort(); };
  }, [currentSession.id, currentSession.experienceId, paneKey]);

  const pane = !currentSession.experienceId ? undefined : error ? (
    <div role="alert" className="flex h-full items-center justify-center gap-2 p-4 text-sm text-destructive">
      <AlertCircle className="size-4 shrink-0" />
      <span>{error}</span>
    </div>
  ) : plugin?.entryTool ? (
    <McpAppFrame key={paneKey} sessionId={currentSession.id} plugin={plugin} />
  ) : (
    <div role="status" className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
      <Loader2 aria-hidden="true" className="size-5 motion-safe:animate-spin" />
      <span>Загрузка приложения…</span>
    </div>
  );

  return (
    <div className="h-full min-h-0 bg-background">
      <AcpSession
        sessionId={currentSession.id}
        pane={pane ? <PaneBoundary key={paneKey}>{pane}</PaneBoundary> : undefined}
        controls={<SessionPaneControl session={currentSession} name={plugin?.name} onChanged={setCurrentSession} />}
        title={<SessionTitle key={currentSession.id} session={currentSession} />}
      />
    </div>
  );
}
