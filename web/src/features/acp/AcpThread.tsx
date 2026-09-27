import {
  Children,
  createContext,
  useCallback,
  useContext,
  type PropsWithChildren,
  type ReactNode,
} from "react";
import { Activity, Download, Loader2, Wrench, ChevronRight, LockKeyhole, FileText, Search, List } from "lucide-react";
import { type ToolCallMessagePartComponent, useAuiState } from "@assistant-ui/react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import { sessionClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  Thread,
  type ThreadGroupPart,
  type ThreadToolPart,
} from "@/components/assistant-ui/thread";
import {
  ComposerUploadContext,
  type UploadFn,
} from "@/components/assistant-ui/composer-upload";
import {
  bareToolName,
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

function AcpToolGroup({
  children,
  group,
}: PropsWithChildren<{ group: ThreadGroupPart }>) {
  const running = group.status.type === "running";
  const content = useAuiState((state) => state.message.content);
  const calls = group.indices.map((index) => content[index] as ThreadToolPart | undefined);
  const items = Children.toArray(children);
  const activity = (parts: ReactNode, count: number, lastCall: ThreadToolPart | undefined, active: boolean, key: number) => (
    <details key={key} className="group/activity rounded-lg border border-border/60 bg-card/30">
      <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 px-3 text-sm text-muted-foreground select-none hover:text-foreground">
        <Activity className="size-4 shrink-0" />
        <span className="font-medium">Активность · {count}</span>
        {active && lastCall?.type === "tool-call" && <span className="min-w-0 truncate text-xs">· {lastCall.toolName.replace(/ File$/i, "")} {toolPath(bareToolArgs(lastCall.args))}</span>}
        {active ? <Loader2 className="ml-auto size-3.5 animate-spin" /> : <ChevronRight className="ml-auto size-4 transition-transform group-open/activity:rotate-90" />}
      </summary>
      <div className="space-y-2 border-t border-border/50 p-2">{parts}</div>
    </details>
  );

  if (items.length !== calls.length) {
    const lastCall = calls[calls.length - 1];
    const same = calls.length >= 3 && calls.every((call) => call?.type === "tool-call" && call.toolName === lastCall?.toolName);
    return <div className="my-2 space-y-2">{same ? activity(children, calls.length, lastCall, running, 0) : children}</div>;
  }

  const rendered: ReactNode[] = [];
  for (let start = 0; start < calls.length;) {
    let end = start + 1;
    while (end < calls.length && calls[end]?.toolName === calls[start]?.toolName) end++;
    if (end - start >= 3 && calls[start]?.type === "tool-call") {
      rendered.push(activity(items.slice(start, end), end - start, calls[end - 1], running && end === calls.length, start));
    } else {
      rendered.push(...items.slice(start, end));
    }
    start = end;
  }
  return <div className="my-2 space-y-2">{rendered}</div>;
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
import { PlanPanel, type PlanEntry } from "./PlanPanel";
import { BrowserHandoffCard } from "./browser/BrowserHandoffCard/BrowserHandoffCard";
import { BROWSER_HANDOFF_TOOL_NAME } from "./frontendTools";

// AcpThread — лента ACP-чата на готовом компоненте Thread из assistant-ui registry
// (src/components/assistant-ui/thread.tsx). Здесь — только подключение наших
// расширений: семантические карточки инструментов кодинг-агента (diff/терминал/файл),
// frontend-сниппет show_choice и проброс slash-команд агента в composer. Разметка
// сообщений, размышлений, action-bar и composer'а живут в registry-компоненте.
export function AcpThread({
  commands,
  plan,
  usage,
  a2ui,
  configOptions,
  onConfigChange,
  responseProfiles,
  responseProfileId,
  responseProfileBusy,
  onResponseProfileChange,
  permission,
  onPermissionDecision,
  readonly = false,
  sessionId,
  workspace = false,
}: {
  commands: AvailableCommand[];
  plan: PlanEntry[];
  usage?: { used: number; size: number } | null;
  a2ui: A2uiState;
  configOptions: ConfigOption[];
  onConfigChange: (configId: string, value: string) => void;
  responseProfiles?: { id: string; name: string; deleted?: boolean }[];
  responseProfileId?: string;
  responseProfileBusy?: boolean;
  onResponseProfileChange?: (id: string) => void;
  permission?: PendingPermission | null;
  onPermissionDecision?: (decision: string) => void;
  readonly?: boolean;
  sessionId?: string;
  workspace?: boolean;
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
  const isToolStandalone = useCallback(
    (part: ThreadToolPart) => {
      const name = bareToolName(part.toolName);
      return (
        name === SAVE_NOTE_TOOL_NAME ||
        name === RENDER_UI_TOOL_NAME ||
        name === PUBLISH_FILE_TOOL_NAME ||
        name === BROWSER_HANDOFF_TOOL_NAME ||
        FRONTEND_TOOL_NAMES.has(name) ||
        generatedImageFiles(part.result).length > 0 ||
        parseDiffResult(part.result) !== null ||
        a2ui.processor.model.surfacesMap.has(part.toolCallId)
      );
    },
    [a2ui.processor, a2ui.version],
  );

  return (
    <A2uiContext.Provider value={a2ui}>
      <AcpSessionContext.Provider value={sessionId}>
        <ComposerUploadContext.Provider
          value={readonly || !sessionId ? null : uploadFile}
        >
          <Thread
            commands={commands}
            usage={usage}
            components={{
              ToolFallback,
              ToolGroup: AcpToolGroup,
              isToolStandalone,
            }}
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
            responseProfiles={responseProfiles}
            responseProfileId={responseProfileId}
            responseProfileBusy={responseProfileBusy}
            onResponseProfileChange={onResponseProfileChange}
            readonly={readonly}
            workspace={workspace}
          />
        </ComposerUploadContext.Provider>
      </AcpSessionContext.Provider>
    </A2uiContext.Provider>
  );
}

export function PermissionComposer({
  permission,
  onDecide,
}: {
  permission: PendingPermission;
  onDecide: (decision: string) => void;
}) {
  const command = permission.command || /^(?:Run(?:ning)?|Execute|Выполнить)(?: command| команду)?[:：]\s*(.+)$/is.exec(permission.title)?.[1];
  const question = command ? "Разрешить выполнение команды?" : permission.title;
  return (
    <div className="flex w-full min-w-0 flex-col gap-3 rounded-(--composer-radius) border border-warning/45 bg-warning/5 p-3">
      <div className="min-w-0 px-1">
        <div className="mb-1 flex items-center gap-1.5 text-xs font-medium text-warning">
          <LockKeyhole className="size-3.5" /> Агент ждёт разрешения
        </div>
        <div className="text-sm">{question}</div>
        {command && <pre className="mt-2 max-h-32 max-w-full overflow-auto rounded border bg-background p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">{command}</pre>}
      </div>
      <div className="flex min-w-0 flex-wrap justify-end gap-2">
        {[...permission.options].sort((a, b) => {
          const order = (kind?: string) => kind === "allow_once" ? 0 : kind === "allow_always" ? 1 : kind?.startsWith("reject") ? 2 : 1;
          return order(a.kind) - order(b.kind);
        }).map((option) => {
          const reject = option.kind?.startsWith("reject");
          return (
            <Button
              key={option.optionId}
              type="button"
              variant={reject ? "outline" : option.kind === "allow_always" ? "secondary" : "default"}
              className={reject ? "max-w-full border-destructive/45 text-destructive" : "max-w-full"}
              onClick={() => onDecide(option.optionId)}
              title={option.name ?? option.optionId}
            >
              <span className="line-clamp-2 max-w-[min(24rem,70vw)] break-words whitespace-normal">
                {option.name ?? option.optionId}
              </span>
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
// Codex ACP передаёт rawInput транспортным конвертом с server и arguments;
// Claude отдаёт непосредственно arguments. Карточкам нужны аргументы инструмента.
function bareToolArgs<T>(args: T): T {
  if (!args || typeof args !== "object" || Array.isArray(args)) return args;
  const input = args as Record<string, unknown>;
  if (
    typeof input.server === "string" &&
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

  if (toolName === BROWSER_HANDOFF_TOOL_NAME) {
    const reason = (toolProps.args as { reason?: unknown } | undefined)?.reason;
    return <BrowserHandoffCard sessionId={sessionId} result={toolProps.result} reason={typeof reason === "string" ? reason : undefined} />;
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

  const done = props.status.type === "complete" || props.status.type === "incomplete" || props.result !== undefined;
  const running = !done;
  const state = running ? "running" : props.status.type === "incomplete" || (props.result && typeof props.result === "object" && "isError" in props.result && props.result.isError) ? "error" : "ok";

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
  switch (props.toolName.toLowerCase()) {
    case "terminal": {
      const result = terminalResult(props.result);
      return <TerminalCard output={result?.output !== undefined ? result.output : resultText} command={terminalCommand(toolProps.args)} title={props.toolName} state={result?.isError ? "error" : state} />;
    }
    case "read file":
    case "read":
      return <ToolLine name="Read" path={toolPath(toolProps.args)} running={running} />;
    case "search":
      return <ToolLine name="Search" path={toolPath(toolProps.args)} running={running} />;
    case "list":
    case "list files":
      return <ToolLine name="List" path={toolPath(toolProps.args)} running={running} />;
  }

  return <ToolInvocation name={props.toolName} argsText={props.argsText} done={done} />;
};

function terminalCommand(args: unknown): string | undefined {
  if (!args || typeof args !== "object" || Array.isArray(args)) return undefined;
  const value = args as Record<string, unknown>;
  return typeof value.command === "string" ? value.command : typeof value.cmd === "string" ? value.cmd : undefined;
}

function terminalResult(result: unknown): { isError: true; output?: string | null } | null {
  try {
    const value: unknown = typeof result === "string" ? JSON.parse(result) : result;
    if (value && typeof value === "object" && "isError" in value && value.isError === true) {
      return "output" in value ? { isError: true, output: formatResult(value.output) } : { isError: true };
    }
  } catch {
    // Обычный текстовый вывод терминала не является JSON.
  }
  return null;
}

function toolPath(args: unknown): string | undefined {
  if (!args || typeof args !== "object" || Array.isArray(args)) return undefined;
  const value = args as Record<string, unknown>;
  for (const key of ["path", "file_path", "filePath", "pattern", "query"]) {
    if (typeof value[key] === "string") return value[key];
  }
  return undefined;
}

function ToolLine({ name, path, running }: { name: string; path?: string; running: boolean }) {
  const Icon = name.startsWith("Read") ? FileText : name.startsWith("Search") ? Search : List;
  return <div className="my-1 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground"><Icon className="size-3.5 shrink-0" /><span>{name === "Read File" ? "Read" : name}</span>{path && <span className="min-w-0 truncate font-mono text-foreground" title={path}>{path}</span>}{running && <Loader2 className="size-3 animate-spin" />}</div>;
}

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
