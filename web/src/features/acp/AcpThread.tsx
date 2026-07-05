import { useContext } from "react";
import { Loader2, Wrench, ChevronRight } from "lucide-react";
import { type ToolCallMessagePartComponent } from "@assistant-ui/react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Thread } from "@/components/assistant-ui/thread";
import { FRONTEND_TOOL_NAMES, RENDER_UI_TOOL_NAME } from "./frontendTools";
import { A2uiContext } from "./a2ui/context";
import { RenderUiCard } from "./a2ui/renderUi";
import type {
  AvailableCommand,
  A2uiState,
  ConfigOption,
} from "./useAcpRuntime";
import { parseDiffResult } from "./tools/diff";
import { DiffCard } from "./tools/DiffCard";
import { TerminalCard } from "./tools/TerminalCard";
import { FileCard } from "./tools/FileCard";
import { PlanPanel, type PlanEntry } from "./PlanPanel";

// AcpThread — лента ACP-чата на готовом компоненте Thread из assistant-ui registry
// (src/components/assistant-ui/thread.tsx). Здесь — только подключение наших
// расширений: семантические карточки инструментов кодинг-агента (diff/терминал/файл),
// frontend-сниппет show_choice и проброс slash-команд агента в composer. Разметка
// сообщений, размышлений, action-bar и composer'а живут в registry-компоненте.
export function AcpThread({
  commands,
  plan,
  a2ui,
  configOptions,
  onConfigChange,
}: {
  commands: AvailableCommand[];
  plan: PlanEntry[];
  a2ui: A2uiState;
  configOptions: ConfigOption[];
  onConfigChange: (configId: string, value: string) => void;
}) {
  return (
    <A2uiContext.Provider value={a2ui}>
      <Thread
        commands={commands}
        components={{ ToolFallback }}
        footer={<PlanPanel plan={plan} />}
        configOptions={configOptions}
        onConfigChange={onConfigChange}
      />
    </A2uiContext.Provider>
  );
}

// ToolFallback — диспетчер рендера tool-call'ов. Семантические карточки выбираются по
// содержимому результата (структурный diff) и человекочитаемому имени инструмента от
// ACP-адаптера («Terminal», «Read File»); всё прочее — generic-блок с раскрывающимися
// аргументами и результатом.
// bareToolName снимает MCP-префикс `mcp__<server>__` с имени инструмента. Кастомные тулы
// brigade доставляются модели MCP-сервером (см. backend acp.BrigadeMCPServer), поэтому до
// клиента доходит имя вида `mcp__brigade__render_ui`; матчим по «голому» имени, а не по
// полному (устойчиво к имени сервера) и не по человекочитаемому title (адаптер отдаёт сырое
// имя). Не-MCP тулы (Terminal, Read File) остаются как есть.
function bareToolName(name: string): string {
  return name.replace(/^mcp__.*?__/, "");
}

const ToolFallback: ToolCallMessagePartComponent = (props) => {
  const a2ui = useContext(A2uiContext);
  const toolName = bareToolName(props.toolName);

  // render_ui — generative UI от агента: строит и рендерит собственную A2UI-поверхность
  // (со скелетоном при стриминге и error boundary на невалидные пропсы). Обрабатывается
  // до generic-lookup, поэтому его поверхность идёт только через RenderUiCard.
  if (toolName === RENDER_UI_TOOL_NAME) {
    return <RenderUiCard {...props} />;
  }

  // A2UI-поверхность карточки (бэкенд синтезирует её из ACP-событий, surfaceId =
  // toolCallId) имеет приоритет: один серверный формат описания рендерится нативным
  // каталогом платформы. Без поверхности — фолбэк на локальные React-карточки.
  const surface = a2ui?.processor.model.surfacesMap.get(props.toolCallId);
  if (surface) {
    return <A2uiSurface surface={surface} />;
  }

  if (FRONTEND_TOOL_NAMES.has(toolName)) {
    return <SnippetCard {...props} />;
  }

  const done = props.status.type === "complete" || props.result !== undefined;
  const running = !done;

  // Diff определяется по контенту, а не имени: Edit/Write оба несут структурный
  // diff-результат, а «липкий diff» бэкенда гарантирует, что статусная строка его
  // не затёрла.
  const diffs = parseDiffResult(props.result);
  if (diffs) {
    return <DiffCard blocks={diffs} />;
  }

  const resultText =
    props.result === undefined ? null : formatResult(props.result);
  switch (props.toolName) {
    case "Terminal":
      return <TerminalCard output={resultText} running={running} />;
    case "Read File":
      return <FileCard content={resultText} running={running} />;
  }

  const args = prettyArgs(props.argsText);
  const result = resultText;

  return (
    <div className="space-y-2 rounded-lg border border-dashed border-border bg-card/40 p-3">
      <div className="flex items-center gap-2 text-sm">
        <Wrench className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate font-mono font-medium">
          {props.toolName || "tool"}
        </span>
        {done ? (
          <span className="size-1.5 shrink-0 rounded-full bg-success/70" />
        ) : (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}
      </div>
      {args && <Disclosure label="Аргументы" content={args} muted />}
      {result && <Disclosure label="Результат" content={result} />}
    </div>
  );
};

// SnippetCard — рендер демо-сниппета show_choice: заголовок и набор вариантов.
// Аргументы стримятся, поэтому JSON может быть ещё неполным — парсим осторожно.
const SnippetCard: ToolCallMessagePartComponent = (props) => {
  const args = props.args as { title?: string; options?: unknown } | undefined;
  const title = typeof args?.title === "string" ? args.title : props.toolName;
  const options = Array.isArray(args?.options)
    ? args.options.filter((o): o is string => typeof o === "string")
    : [];
  const done = props.status.type === "complete";

  return (
    <div className="space-y-2.5 rounded-lg border bg-card p-4">
      <div className="flex items-center gap-2">
        <span className="inline-flex items-center gap-1 rounded-md bg-secondary px-2 py-0.5 text-xs font-medium text-secondary-foreground">
          <Wrench className="size-3" />
          сниппет
        </span>
        <span className="text-sm font-medium">{title}</span>
      </div>
      <div className="flex flex-wrap gap-2">
        {options.length === 0 && !done ? (
          <span className="text-xs text-muted-foreground">загрузка…</span>
        ) : (
          options.map((opt, i) => (
            <Button key={i} variant="outline" size="sm">
              <ChevronRight className="size-3.5" />
              {opt}
            </Button>
          ))
        )}
      </div>
    </div>
  );
};

// Disclosure — свёрнутый блок аргументов/результата tool-call. Содержимое
// моноширинное со своим горизонтальным скроллом, чтобы длинные строки не
// растягивали карточку.
function Disclosure({
  label,
  content,
  muted,
}: {
  label: string;
  content: string;
  muted?: boolean;
}) {
  return (
    <details className="group rounded bg-muted/50">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 px-2 py-1 text-xs text-muted-foreground select-none">
        <ChevronRight className="size-3 transition-transform group-open:rotate-90" />
        <span className="font-medium">{label}</span>
      </summary>
      <pre
        className={cn(
          "max-h-72 overflow-auto border-t border-border/50 px-2 py-1.5 font-mono text-xs leading-relaxed whitespace-pre",
          muted ? "text-muted-foreground" : "text-foreground",
        )}
      >
        {content}
      </pre>
    </details>
  );
}

// prettyArgs приводит сырой текст аргументов tool-call к читаемому виду. Аргументы
// стримятся строкой: валидный JSON форматируем с отступами, пустой объект считаем
// отсутствием аргументов, недостроенную строку показываем как есть.
function prettyArgs(argsText: string): string | null {
  const raw = argsText.trim();
  if (!raw || raw === "{}" || raw === "[]" || raw === "null") return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (parsed === null) return null;
    if (typeof parsed === "object" && Object.keys(parsed).length === 0) {
      return null;
    }
    return JSON.stringify(parsed, null, 2);
  } catch {
    return raw;
  }
}

// formatResult приводит результат tool-call (строка/объект) к читаемой строке.
function formatResult(result: unknown): string | null {
  if (result == null) return null;
  if (typeof result === "string") return result.trim() || null;
  try {
    return JSON.stringify(result, null, 2);
  } catch {
    return String(result);
  }
}
