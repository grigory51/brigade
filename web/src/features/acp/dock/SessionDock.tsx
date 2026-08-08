import { useEffect, useMemo, useState, type ComponentType } from "react";
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

/**
 * SessionDock — плавающая обвязка страницы ACP-сессии: чипы управления окнами сверху
 * справа, шкала навигации по ленте у правого края и сами окна (ссылки, терминал).
 * Чат под ними занимает всю ширину и ничего не отдаёт панелям.
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
}: {
  sessionId: string;
  // readonly — архивная сессия: лента та же, но живого агента за ней нет. Шкала и ссылки
  // работают как обычно, терминал и dev-серверы недоступны — их некому обслуживать.
  readonly?: boolean;
}) {
  const [panel, setPanel] = useState<"" | "links" | "mcp">("");
  const [terminal, setTerminal] = useState(loadTerminalOpen);
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

  return (
    <>
      <MsgNav />

      {/* right-[66px] — правее шкалы навигации; там, где её нет, чипы прижимаются к краю. */}
      <div className="absolute top-3.5 right-3 z-30 flex animate-[chip-in_0.3s_ease] gap-2 lg:right-[66px]">
        {!readonly && <SessionDesktopTools sessionId={sessionId} sessionName={sessionName} />}
        <Chip
          icon={Link2}
          label="Ссылки"
          active={panel === "links"}
          onClick={() => setPanel((p) => (p === "links" ? "" : "links"))}
        >
          <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-lg bg-secondary px-1 text-[10px] text-muted-foreground">
            {linksCount}
          </span>
        </Chip>
        {!readonly && (
          <Chip
            icon={Plug}
            label="MCP"
            active={panel === "mcp"}
            onClick={() => setPanel((p) => (p === "mcp" ? "" : "mcp"))}
          >
            <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-lg bg-secondary px-1 text-[10px] text-muted-foreground">
              {mcpEnabled.length}
            </span>
          </Chip>
        )}
        {!readonly && (
          <Chip
            icon={SquareTerminal}
            label="Терминал"
            active={terminal}
            onClick={() => setTerminal((v) => !v)}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                terminal ? "bg-success" : "bg-muted-foreground/50",
              )}
            />
          </Chip>
        )}
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
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  active: boolean;
  onClick: () => void;
  children?: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      // Метка для окон, закрывающихся по клику мимо: клик по своему чипу — это
      // переключение, а не «мимо» (см. LinksWindow).
      data-dock-chip
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border bg-[rgba(31,30,29,0.85)] px-3 py-1.5 text-xs shadow-[0_4px_14px_rgba(0,0,0,0.3)] backdrop-blur-[8px] transition-colors hover:border-[#5a4034] hover:text-foreground",
        active
          ? "border-[#5a4034] text-foreground"
          : "border-[rgba(65,63,59,0.9)] text-muted-foreground",
      )}
    >
      <Icon className="size-3.5" />
      <span className="hidden sm:inline">{label}</span>
      {children}
    </button>
  );
}
