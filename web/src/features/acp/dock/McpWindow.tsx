import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2, Plug } from "lucide-react";
import { toast } from "sonner";
import { ConnectError } from "@connectrpc/connect";
import { mcpClient, sessionClient } from "@/api/client";
import { McpTransport, type McpServer } from "@/api/gen/brigade/v1/mcp_pb";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  FloatingWindow,
  WindowTitlebar,
  useDismissOnOutside,
} from "./FloatingWindow";

/**
 * McpWindow — плавающее окно «MCP»: какие MCP-серверы пользователя подключены к этой
 * сессии. Отметки применяются кнопкой, а не сразу: смена набора переинициализирует
 * агента (переписка сохраняется, инструменты меняются), и делать это на каждый клик
 * незачем.
 */

const TRANSPORT_LABEL: Record<number, string> = {
  [McpTransport.STDIO]: "stdio",
  [McpTransport.HTTP]: "http",
  [McpTransport.SSE]: "sse",
};

export function McpWindow({
  sessionId,
  enabled,
  onApplied,
  onClose,
}: {
  sessionId: string;
  // enabled — набор, сохранённый у сессии; окно правит свою копию до «Применить».
  enabled: string[];
  onApplied: (ids: string[]) => void;
  onClose: () => void;
}) {
  const windowRef = useRef<HTMLDivElement>(null);
  useDismissOnOutside(windowRef, onClose);

  const [servers, setServers] = useState<McpServer[] | null>(null);
  const [draft, setDraft] = useState<string[]>(enabled);
  const [applying, setApplying] = useState(false);

  useEffect(() => {
    let cancelled = false;
    mcpClient
      .listServers({})
      .then((res) => !cancelled && setServers(res.servers))
      .catch(() => !cancelled && setServers([]));
    return () => {
      cancelled = true;
    };
  }, []);

  const apply = useCallback(async () => {
    setApplying(true);
    try {
      await sessionClient.setSessionMcpServers({
        sessionId,
        mcpServerIds: draft,
      });
      onApplied(draft);
      toast.success("Набор MCP применён");
      onClose();
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось применить набор",
      );
    } finally {
      setApplying(false);
    }
  }, [draft, sessionId, onApplied, onClose]);

  const changed =
    draft.length !== enabled.length ||
    draft.some((id) => !enabled.includes(id));

  return (
    <FloatingWindow
      ref={windowRef}
      className="top-[54px] right-3 z-20 max-h-[min(420px,calc(100%-4.5rem))] w-[320px] max-w-[calc(100%-1.5rem)] lg:right-[66px]"
      titlebar={
        <WindowTitlebar icon={Plug} title="MCP-серверы" onClose={onClose} />
      }
    >
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {servers === null ? (
          <div className="flex items-center justify-center py-6">
            <Loader2 className="size-4 animate-spin text-muted-foreground" />
          </div>
        ) : servers.length === 0 ? (
          <p className="px-2 py-4 text-center text-[12px] leading-[1.6] text-muted-foreground">
            Серверов пока нет. Добавьте их в разделе
            <br />
            <span className="text-foreground">Настройки → MCP-серверы</span>
          </p>
        ) : (
          servers.map((srv) => {
            const on = draft.includes(srv.id);
            return (
              <label
                key={srv.id}
                className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors hover:bg-secondary"
              >
                <Checkbox
                  checked={on}
                  onCheckedChange={() =>
                    setDraft((prev) =>
                      on ? prev.filter((id) => id !== srv.id) : [...prev, srv.id],
                    )
                  }
                />
                <span className="min-w-0 flex-1 truncate text-[12.5px]">
                  {srv.name}
                </span>
                <span className="shrink-0 rounded-[5px] bg-secondary px-1.5 text-[10px] text-muted-foreground">
                  {TRANSPORT_LABEL[srv.transport] ?? "?"}
                </span>
              </label>
            );
          })
        )}
      </div>

      {servers !== null && servers.length > 0 && (
        <div className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2.5">
          <span className="min-w-0 flex-1 text-[11px] leading-[1.4] text-muted-foreground/70">
            Агент перезагрузится, переписка сохранится
          </span>
          <Button size="sm" disabled={!changed || applying} onClick={() => void apply()}>
            {applying && <Loader2 className="size-3.5 animate-spin" />}
            Применить
          </Button>
        </div>
      )}
    </FloatingWindow>
  );
}
