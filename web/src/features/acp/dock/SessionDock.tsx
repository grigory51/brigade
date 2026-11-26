import { useEffect, useMemo, useState, type ComponentType, type ReactNode } from "react";
import { Link2, Plug, SquareTerminal } from "lucide-react";
import { useAuiState } from "@assistant-ui/react";
import { sessionClient } from "@/api/client";
import { usePreviews } from "@/features/sessions/PreviewLinks";
import { SessionDesktopTools } from "@/features/desktop/SessionDesktopTools";
import { cn } from "@/lib/utils";
import { extractLinks } from "./links";
import { LinksWindow } from "./LinksWindow";
import { McpWindow } from "./McpWindow";
import { MsgNav } from "./MsgNav";
import { TerminalWindow } from "./TerminalWindow";
import { DockChip } from "./DockChip";
import type { TermConnState } from "@/features/terminal/TerminalView";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * Общие инструменты сессии и окна ссылок, MCP и терминала. В живой сессии панель
 * занимает строку над содержимым; в архиве остаётся поверх ленты.
 *
 * Монтируется внутри AssistantRuntimeProvider: шкале и ссылкам нужна лента сообщений.
 */

const STORE_KEY = "brigade.dock";

// Переживает перезагрузку только терминал: он часть рабочего места, и держать его
// открытым — осознанный выбор. Справочные окна (ссылки) открываются под конкретный
// вопрос и всплывать сами при следующем заходе не должны. Терминал стартует закрытым
// до первого открытия: его раскрытие спавнит pty в среде агента.
function loadTerminalOpen(): boolean {
  try {
    const raw = localStorage.getItem(STORE_KEY);
    return raw ? (JSON.parse(raw) as { terminal?: boolean }).terminal === true : false;
  } catch {
    // Повреждённое или недоступное хранилище — просто стартуем с дефолта.
    return false;
  }
}

export function SessionDock({
  sessionId,
  readonly = false,
  controls,
  placement = "floating",
  navigation = true,
  title,
}: {
  sessionId: string;
  // readonly — архивная сессия: лента та же, но живого агента за ней нет. Шкала и ссылки
  // работают как обычно, терминал и dev-серверы недоступны — их некому обслуживать.
  readonly?: boolean;
  controls?: ReactNode;
  placement?: "floating" | "flow";
  navigation?: boolean;
  title?: ReactNode;
}) {
  const [panel, setPanel] = useState<"" | "links" | "mcp">("");
  const [terminal, setTerminal] = useState(loadTerminalOpen);
  const [terminalConnection, setTerminalConnection] = useState<TermConnState>("connecting");
  const messages = useAuiState((s) => s.thread.messages);
  const previews = usePreviews(readonly ? "" : sessionId);
  // Набор MCP-серверов сессии: нужен чипу для счётчика и окну как исходное состояние.
  const [mcpEnabled, setMcpEnabled] = useState<string[]>([]);
  const [sessionName, setSessionName] = useState(sessionId);

  useEffect(() => {
    if (readonly) return;
    let cancelled = false;
    sessionClient
      .get({ sessionId })
      .then((res) => {
        if (!cancelled) {
          setMcpEnabled(res.session?.mcpServerIds ?? []);
          setSessionName(res.session?.name || sessionId);
        }
      })
      .catch(() => {
        // Набор не загрузился — чип покажет ноль, окно откроет актуальный список сам.
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId, readonly]);

  useEffect(() => {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify({ terminal }));
    } catch {
      // Приватный режим/переполнение — состояние просто не переживёт перезагрузку.
    }
  }, [terminal]);

  const links = useMemo(() => {
    const previewUrls = new Set(previews.map((p) => p.url));
    return extractLinks(messages, previewUrls);
  }, [messages, previews]);

  const linksCount = links.length + previews.length;
  const terminalStatus = !terminal ? "Не запущен" : {
    connecting: "Подключение…", open: "Подключён", closed: "Отключён", error: "Ошибка подключения",
  }[terminalConnection];

  return (
    <>
      {navigation && <MsgNav />}

      {/* right-[66px] — правее шкалы навигации; там, где её нет, чипы прижимаются к краю. */}
      <div aria-label="Инструменты сессии" className={cn(
        "z-30 flex flex-wrap items-center gap-2",
        placement === "flow"
          ? "relative shrink-0 justify-start border-b bg-background py-2 pr-3 pl-[var(--session-toolbar-inset,1rem)]"
          : "absolute top-3.5 right-3 left-12 justify-end lg:right-[66px]",
      )}>
        {title && <div className="min-w-0 max-w-full sm:max-w-64">{title}</div>}
        <div className={cn("flex min-w-0 flex-wrap items-center gap-2", placement === "flow" ? "justify-start" : "justify-end")}>
        {controls}
        {!readonly && <SessionDesktopTools sessionId={sessionId} sessionName={sessionName} />}
        <Chip
          icon={Link2}
          label="Ссылки"
          active={panel === "links"}
          onClick={() => setPanel((p) => (p === "links" ? "" : "links"))}
        >
          {linksCount > 0 && <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-lg bg-secondary px-1 text-[10px] text-muted-foreground">
            {linksCount}
          </span>}
        </Chip>
        {!readonly && (
          <Chip
            icon={Plug}
            label="MCP"
            active={panel === "mcp"}
            onClick={() => setPanel((p) => (p === "mcp" ? "" : "mcp"))}
          >
            {mcpEnabled.length > 0 && <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-lg bg-secondary px-1 text-[10px] text-muted-foreground">
              {mcpEnabled.length}
            </span>}
          </Chip>
        )}
        {!readonly && (
          <Chip
            icon={SquareTerminal}
            label="Терминал"
            description={`Терминал: ${terminalStatus}`}
            active={terminal}
            onClick={() => { setTerminalConnection("connecting"); setTerminal((v) => !v); }}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                !terminal || terminalConnection === "closed" ? "bg-muted-foreground" : terminalConnection === "open" ? "bg-success" : terminalConnection === "error" ? "bg-destructive" : "bg-warning motion-safe:animate-pulse",
              )}
            />
          </Chip>
        )}
        </div>
      </div>

      {panel === "links" && (
        <LinksWindow
          links={links}
          previews={previews}
          onClose={() => setPanel("")}
        />
      )}

      {panel === "mcp" && !readonly && (
        <McpWindow
          sessionId={sessionId}
          enabled={mcpEnabled}
          onApplied={setMcpEnabled}
          onClose={() => setPanel("")}
        />
      )}

      {terminal && !readonly && (
        <TerminalWindow
          sessionId={sessionId}
          onClose={() => setTerminal(false)}
          onConnChange={setTerminalConnection}
        />
      )}
    </>
  );
}

// Chip — стеклянная капсула управления окном: иконка, подпись и правый индикатор
// (счётчик или точка состояния).
function Chip({
  icon: Icon,
  label,
  active,
  onClick,
  children,
  description,
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  active: boolean;
  onClick: () => void;
  children?: React.ReactNode;
  description?: string;
}) {
  return (
    <Tooltip><TooltipTrigger asChild><DockChip
      aria-label={label}
      aria-pressed={active}
      active={active}
      onClick={onClick}
    >
      <Icon className="size-3.5" />
      <span className="hidden sm:inline">{label}</span>
      {children}
    </DockChip></TooltipTrigger><TooltipContent className="pointer-events-none">{description || label}</TooltipContent></Tooltip>
  );
}
