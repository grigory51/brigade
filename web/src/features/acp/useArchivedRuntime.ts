import { useEffect, useMemo, useState } from "react";
import { HttpAgent } from "@ag-ui/client";
import { fromAgUiMessages, useAgUiRuntime } from "@assistant-ui/react-ag-ui";
import {
  ExportedMessageRepository,
  type ThreadHistoryAdapter,
} from "@assistant-ui/react";
import { MessageProcessor } from "@a2ui/web_core/v0_9";
import type { ReactComponentImplementation } from "@a2ui/react/v0_9";
import { archiveClient } from "@/api/client";
import { cardsCatalog, basicCatalog } from "./a2ui/catalog";
import {
  assembleHistory,
  type A2uiState,
  type HistoryMessage,
} from "./useAcpRuntime";

// useArchivedRuntime — рантайм для readonly-просмотра архивной сессии. В отличие от
// живого useAcpRuntime здесь нет SSE, поллинга статуса, permission и composer'а: лента
// поднимается один раз из снимка истории (ArchiveService.GetHistory), агент не работает.
// A2UI-процессор нужен для рендера render_ui-карточек (RenderUiCard строит поверхность из
// argsText снимка); diff-карточки рисуются из result. Агент (HttpAgent) формально нужен
// useAgUiRuntime, но не вызывается — composer скрыт (AcpThread readonly).
export function useArchivedRuntime(sessionId: string): {
  runtime: ReturnType<typeof useAgUiRuntime>;
  a2ui: A2uiState;
} {
  const a2uiProcessor = useMemo(
    () => new MessageProcessor<ReactComponentImplementation>([cardsCatalog, basicCatalog]),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sessionId],
  );
  const [a2uiVersion, setA2uiVersion] = useState(0);
  useEffect(() => {
    const bump = () => setA2uiVersion((v) => v + 1);
    const created = a2uiProcessor.onSurfaceCreated(bump);
    const deleted = a2uiProcessor.onSurfaceDeleted(bump);
    return () => {
      created.unsubscribe();
      deleted.unsubscribe();
    };
  }, [a2uiProcessor]);

  // Заглушка-агент: useAgUiRuntime требует агента, но в readonly он не вызывается
  // (нет поля ввода). credentials на всякий случай, если рантайм дёрнет replay-load.
  const agent = useMemo(
    () =>
      new HttpAgent({
        url: "/api/ag-ui/run",
        threadId: sessionId,
        fetch: (url, init) => fetch(url, { ...init, credentials: "include" }),
      }),
    [sessionId],
  );

  const history = useMemo<ThreadHistoryAdapter>(
    () => ({
      load: async () => {
        let data;
        try {
          data = await archiveClient.getHistory({ sessionId });
        } catch {
          return { messages: [] };
        }
        const raw: HistoryMessage[] = data.messages.map((m) => ({
          id: m.id,
          role: m.role,
          content: m.content,
          toolName: m.toolName || undefined,
          argsText: m.argsText || undefined,
          result: m.result,
        }));
        return ExportedMessageRepository.fromArray(
          fromAgUiMessages(assembleHistory(raw), { showThinking: true }),
        );
      },
      append: async () => {},
    }),
    [sessionId],
  );

  const runtime = useAgUiRuntime({
    agent,
    showThinking: true,
    adapters: { history },
  });

  return { runtime, a2ui: { processor: a2uiProcessor, version: a2uiVersion } };
}
