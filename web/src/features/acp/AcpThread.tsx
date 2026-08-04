import {
  createContext,
  useCallback,
  useContext,
  type PropsWithChildren,
} from "react";
import { Download, Loader2, Wrench, ChevronRight } from "lucide-react";
import { type ToolCallMessagePartComponent } from "@assistant-ui/react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import { sessionClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Thread, type ThreadGroupPart } from "@/components/assistant-ui/thread";
import {
  ComposerUploadContext,
  type UploadFn,
} from "@/components/assistant-ui/composer-upload";
import {
  FRONTEND_TOOL_NAMES,
  PUBLISH_FILE_TOOL_NAME,
  RENDER_UI_TOOL_NAME,
  SAVE_NOTE_TOOL_NAME,
} from "./frontendTools";
import { A2uiContext } from "./a2ui/context";
import { RenderUiCard } from "./a2ui/renderUi";
import { PublishFileCard } from "./a2ui/PublishFileCard";
import { SaveNoteCard } from "./SaveNoteCard";

// AcpSessionContext — id текущей ACP-сессии для tool-карточек (SaveNoteCard шлёт его как
// провенанс session заметки). undefined в readonly-архиве / вне сессии.
const AcpSessionContext = createContext<string | undefined>(undefined);

// AcpToolGroup заменяет дефолтный аккордеон «N tool calls»: все вызовы тулов рендерятся
// ПЛОСКО, прямо в ленте — карточки (save_note, render_ui/A2UI, diff, терминал, файл) и
// generic-блоки видны сразу, а не спрятаны в свёртке. Детей рисуем как есть.
function AcpToolGroup({ children }: PropsWithChildren<{ group: ThreadGroupPart }>) {
  return <div className="space-y-2">{children}</div>;
}
import type {
  AvailableCommand,
  A2uiState,
  ConfigOption,
  PendingPermission,
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
  permission,
  onPermissionDecision,
  readonly = false,
  sessionId,
}: {
  commands: AvailableCommand[];
  plan: PlanEntry[];
  a2ui: A2uiState;
  configOptions: ConfigOption[];
  onConfigChange: (configId: string, value: string) => void;
  permission?: PendingPermission | null;
  onPermissionDecision?: (decision: string) => void;
  readonly?: boolean;
  sessionId?: string;
}) {
  // uploadFile заливает вложение в рабочую директорию агента сессии; путь возвращается для
  // вставки в текст сообщения (агент читает файл сам). В readonly-ленте архива недоступно.
  const uploadFile = useCallback<UploadFn>(
    async (file) => {
      const content = new Uint8Array(await file.arrayBuffer());
      const res = await sessionClient.uploadFile({
        sessionId: sessionId ?? "",
        filename: file.name,
        content,
      });
      return res.path;
    },
    [sessionId],
  );

  return (
    <A2uiContext.Provider value={a2ui}>
      <AcpSessionContext.Provider value={sessionId}>
        <ComposerUploadContext.Provider
          value={readonly || !sessionId ? null : uploadFile}
        >
          <Thread
            commands={commands}
            components={{ ToolFallback, ToolGroup: AcpToolGroup }}
            footer={readonly ? undefined : <PlanPanel plan={plan} />}
            composer={
              permission && onPermissionDecision ? (
                <PermissionComposer
                  permission={permission}
                  onDecide={onPermissionDecision}
                />
              ) : undefined
            }
            configOptions={configOptions}
            onConfigChange={onConfigChange}
            readonly={readonly}
          />
        </ComposerUploadContext.Provider>
      </AcpSessionContext.Provider>
    </A2uiContext.Provider>
  );
}

function PermissionComposer({
  permission,
  onDecide,
}: {
  permission: PendingPermission;
  onDecide: (decision: string) => void;
}) {
  return (
    <div className="border-border/60 dark:border-muted-foreground/15 flex w-full flex-col gap-3 rounded-(--composer-radius) border bg-(--composer-bg) p-3 shadow-[0_4px_16px_-8px_rgba(0,0,0,0.08),0_1px_2px_rgba(0,0,0,0.04)] dark:shadow-none">
      <div className="px-1">
        <div className="text-muted-foreground mb-1 text-xs font-medium">
          Агент ждёт разрешения
        </div>
        <pre className="max-h-32 overflow-auto font-mono text-sm leading-relaxed break-all whitespace-pre-wrap">
          {permission.title}
        </pre>
      </div>
      <div className="flex flex-wrap justify-end gap-2">
        {permission.options.map((option) => {
          const reject = option.kind?.startsWith("reject");
          return (
            <Button
              key={option.optionId}
              type="button"
              variant={reject ? "outline" : "default"}
              className={reject ? "text-destructive" : undefined}
              onClick={() => onDecide(option.optionId)}
            >
              {option.name ?? option.optionId}
            </Button>
          );
        })}
      </div>
    </div>
  );
}

// ToolFallback — диспетчер рендера tool-call'ов. Семантические карточки выбираются по
// содержимому результата (структурный diff) и человекочитаемому имени инструмента от
// ACP-адаптера («Terminal», «Read File»); всё прочее — generic-блок с раскрывающимися
// аргументами и результатом.
// bareToolName снимает MCP-префикс с имени инструмента. Claude использует
// `mcp__<server>__<tool>`, Codex ACP — `mcp.<server>.<tool>`.
function bareToolName(name: string): string {
  return name.replace(/^mcp__.*?__/, "").replace(/^mcp\.[^.]+\./, "");
}

// Codex ACP передаёт rawInput MCP-вызова транспортным конвертом
// {server, tool, arguments}; Claude отдаёт непосредственно arguments. Карточкам нужен
// единый внутренний формат — только аргументы конкретного инструмента.
function bareToolArgs<T>(args: T): T {
  if (!args || typeof args !== "object" || Array.isArray(args)) return args;
  const input = args as Record<string, unknown>;
  if (
    input.server === "brigade" &&
    typeof input.arguments === "object" &&
    input.arguments !== null &&
    !Array.isArray(input.arguments)
  ) {
    return input.arguments as T;
  }
  return args;
}

const ToolFallback: ToolCallMessagePartComponent = (props) => {
  const a2ui = useContext(A2uiContext);
  const sessionId = useContext(AcpSessionContext);
  const toolName = bareToolName(props.toolName);
  const toolProps = { ...props, args: bareToolArgs(props.args) };

  // save_note — нативная карточка добавления заметки (навык /note). Сохраняет НАПРЯМУЮ через
  // brigade API, без агента. Ждём завершения tool-call'а, чтобы взять полный черновик из args
  // (при стриминге args ещё частичны, а форма инициализируется из них один раз).
  if (toolName === SAVE_NOTE_TOOL_NAME) {
    if (props.status.type !== "complete") {
      return (
        <div className="rounded-lg border bg-card p-4 text-sm text-muted-foreground">
          Готовлю карточку…
        </div>
      );
    }
    return (
      <SaveNoteCard
        args={toolProps.args as Parameters<typeof SaveNoteCard>[0]["args"]}
        sessionId={sessionId}
      />
    );
  }

  // render_ui — generative UI от агента: строит и рендерит собственную A2UI-поверхность
  // (со скелетоном при стриминге и error boundary на невалидные пропсы). Обрабатывается
  // до generic-lookup, поэтому его поверхность идёт только через RenderUiCard.
  if (toolName === RENDER_UI_TOOL_NAME) {
    return <RenderUiCard {...toolProps} />;
  }

  if (toolName === PUBLISH_FILE_TOOL_NAME) {
    return <PublishFileCard {...toolProps} />;
  }

  // A2UI-поверхность карточки (бэкенд синтезирует её из ACP-событий, surfaceId =
  // toolCallId) имеет приоритет: один серверный формат описания рендерится нативным
  // каталогом платформы. Без поверхности — фолбэк на локальные React-карточки.
  const surface = a2ui?.processor.model.surfacesMap.get(props.toolCallId);
  if (surface) {
    return <A2uiSurface surface={surface} />;
  }

  if (FRONTEND_TOOL_NAMES.has(toolName)) {
    return <SnippetCard {...toolProps} />;
  }

  const done = props.status.type === "complete" || props.result !== undefined;
  const running = !done;

  const generatedImages = generatedImageFiles(props.result);
  if (generatedImages.length > 0 && sessionId) {
    return (
      <div className="space-y-3">
        <ToolInvocation name={props.toolName} argsText={props.argsText} done={done} />
        <div className="grid gap-3">
          {generatedImages.map((image) => {
            const path = image.path
              .split("/")
              .map(encodeURIComponent)
              .join("/");
            const url = `/api/sessions/${encodeURIComponent(sessionId)}/files/${path}?inline=1`;
            return (
              <div key={image.path} className="group relative overflow-hidden rounded-xl border bg-card">
                <img
                  src={url}
                  alt="Сгенерированное изображение"
                  className="max-h-[70vh] w-full object-contain"
                />
                <Button
                  asChild
                  size="icon"
                  variant="secondary"
                  className="absolute right-3 top-3 opacity-0 shadow-md transition-opacity group-hover:opacity-100 focus-within:opacity-100"
                >
                  <a href={url} download aria-label="Скачать изображение">
                    <Download className="size-4" />
                  </a>
                </Button>
              </div>
            );
          })}
        </div>
      </div>
    );
  }

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

  return <ToolInvocation name={props.toolName} argsText={props.argsText} done={done} />;
};

function ToolInvocation({
  name,
  argsText,
  done,
}: {
  name: string;
  argsText?: string;
  done: boolean;
}) {
  const args = prettyArgs(argsText);
  return (
    <div className="space-y-2 rounded-lg border border-dashed border-border bg-card/40 p-3">
      <div className="flex items-center gap-2 text-sm">
        <Wrench className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate font-mono font-medium">
          {name || "tool"}
        </span>
        {done ? (
          <span className="size-1.5 shrink-0 rounded-full bg-success/70" />
        ) : (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}
      </div>
      {args && <Disclosure label="Аргументы" content={args} muted />}
    </div>
  );
}

function generatedImageFiles(result: unknown): { path: string; mimeType: string }[] {
  try {
    const value = typeof result === "string" ? JSON.parse(result) : result;
    if (!value || typeof value !== "object" || (value as { type?: unknown }).type !== "generated_images") {
      return [];
    }
    const images = (value as { images?: unknown }).images;
    if (!Array.isArray(images)) return [];
    return images.filter(
      (image): image is { path: string; mimeType: string } =>
        !!image &&
        typeof image === "object" &&
        typeof (image as { path?: unknown }).path === "string" &&
        typeof (image as { mimeType?: unknown }).mimeType === "string",
    );
  } catch {
    return [];
  }
}

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
function prettyArgs(argsText?: string): string | null {
  const raw = argsText?.trim() ?? "";
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
