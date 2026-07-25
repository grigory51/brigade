import { useEffect, useMemo, useState, type ComponentType } from "react";
import { Link2, SquareTerminal } from "lucide-react";
import { useAuiState } from "@assistant-ui/react";
import { usePreviews } from "@/features/sessions/PreviewLinks";
import { cn } from "@/lib/utils";
import { extractLinks } from "./links";
import { LinksWindow } from "./LinksWindow";
import { MsgNav } from "./MsgNav";
import { TerminalWindow } from "./TerminalWindow";

/**
 * SessionDock — плавающая обвязка страницы ACP-сессии: чипы управления окнами сверху
 * справа, шкала навигации по ленте у правого края и сами окна (ссылки, терминал).
 * Чат под ними занимает всю ширину и ничего не отдаёт панелям.
 *
 * Монтируется внутри AssistantRuntimeProvider: шкале и ссылкам нужна лента сообщений.
 */

type DockState = {
  // panel — какое из взаимоисключающих окон открыто (в фазе 1 их одно).
  panel: "" | "links";
  terminal: boolean;
};

const STORE_KEY = "brigade.dock";

// Состояние окон общее для всех сессий и переживает перезагрузку страницы. Терминал
// по умолчанию закрыт: его раскрытие спавнит pty в среде агента.
function loadState(): DockState {
  try {
    const raw = localStorage.getItem(STORE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<DockState>;
      return {
        panel: parsed.panel === "links" ? "links" : "",
        terminal: parsed.terminal === true,
      };
    }
  } catch {
    // Повреждённое или недоступное хранилище — просто стартуем с дефолта.
  }
  return { panel: "", terminal: false };
}

export function SessionDock({ sessionId }: { sessionId: string }) {
  const [dock, setDock] = useState<DockState>(loadState);
  const messages = useAuiState((s) => s.thread.messages);
  const previews = usePreviews(sessionId);

  useEffect(() => {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify(dock));
    } catch {
      // Приватный режим/переполнение — состояние просто не переживёт перезагрузку.
    }
  }, [dock]);

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
        <Chip
          icon={Link2}
          label="Ссылки"
          active={dock.panel === "links"}
          onClick={() =>
            setDock((d) => ({ ...d, panel: d.panel === "links" ? "" : "links" }))
          }
        >
          <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-lg bg-secondary px-1 text-[10px] text-muted-foreground">
            {linksCount}
          </span>
        </Chip>
        <Chip
          icon={SquareTerminal}
          label="Терминал"
          active={dock.terminal}
          onClick={() => setDock((d) => ({ ...d, terminal: !d.terminal }))}
        >
          <span
            className={cn(
              "size-1.5 rounded-full",
              dock.terminal ? "bg-success" : "bg-muted-foreground/50",
            )}
          />
        </Chip>
      </div>

      {dock.panel === "links" && (
        <LinksWindow
          links={links}
          previews={previews}
          onClose={() => setDock((d) => ({ ...d, panel: "" }))}
        />
      )}

      {dock.terminal && (
        <TerminalWindow
          sessionId={sessionId}
          onClose={() => setDock((d) => ({ ...d, terminal: false }))}
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
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border bg-[rgba(31,30,29,0.85)] px-3 py-1.5 text-xs shadow-[0_4px_14px_rgba(0,0,0,0.3)] backdrop-blur-[8px] transition-colors hover:border-[#5a4034] hover:text-foreground",
        active
          ? "border-[#5a4034] text-foreground"
          : "border-[rgba(65,63,59,0.9)] text-muted-foreground",
      )}
    >
      <Icon className="size-3.5" />
      {label}
      {children}
    </button>
  );
}
