"use client";

import { UserMessageAttachments } from "@/components/assistant-ui/attachment";
import { ComposerUploadButton } from "@/components/assistant-ui/composer-upload";
import {
  ComposerContextZone,
  useComposerContextSend,
} from "@/components/assistant-ui/composer-context";
import { MarkdownText } from "@/components/assistant-ui/markdown-text";
import { LinkWithPreview } from "@/components/assistant-ui/link-with-preview";
import {
  Reasoning,
  ReasoningContent,
  ReasoningRoot,
  ReasoningText,
  ReasoningTrigger,
} from "@/components/assistant-ui/reasoning";
import { TooltipIconButton } from "@/components/assistant-ui/tooltip-icon-button";
import { Button } from "@/components/ui/button";
import {
  getComposerSubmitMode,
  shouldSubmitComposer,
} from "@/lib/composer-submit";
import { cn } from "@/lib/utils";
import {
  ActionBarMorePrimitive,
  ActionBarPrimitive,
  AuiIf,
  type AssistantState,
  ComposerPrimitive,
  ErrorPrimitive,
  groupPartByType,
  MessagePrimitive,
  ThreadPrimitive,
  type ToolCallMessagePartComponent,
  useAuiState,
  useComposer,
  useComposerRuntime,
  useMessagePartText,
} from "@assistant-ui/react";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  CheckIcon,
  ChevronDownIcon,
  CopyIcon,
  DownloadIcon,
  InfoIcon,
  Loader2Icon,
  MicIcon,
  MoreHorizontalIcon,
  SquareIcon,
} from "lucide-react";
import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type FC,
  type KeyboardEvent as ReactKeyboardEvent,
  type PropsWithChildren,
  type ReactNode,
} from "react";
import type {
  AvailableCommand,
  ConfigOption,
} from "@/features/acp/useAcpRuntime";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export type ThreadGroupPart = MessagePrimitive.GroupedParts.GroupPart;

const defaultMessageGroupBy = groupPartByType({
  reasoning: ["group-chainOfThought", "group-reasoning"],
  "tool-call": ["group-chainOfThought", "group-tool"],
  "standalone-tool-call": [],
});

export type ThreadToolPart = Extract<
  Parameters<typeof defaultMessageGroupBy>[0],
  { type: "tool-call" }
>;

/**
 * Переопределения рендера ленты. Оба слота обязательны по смыслу: карточки
 * инструментов и их группировку задаёт ACP-слой (см. features/acp/AcpThread).
 */
export type ThreadComponents = {
  ToolFallback: ToolCallMessagePartComponent;
  ToolGroup: ComponentType<PropsWithChildren<{ group: ThreadGroupPart }>>;
  isToolStandalone?: (part: ThreadToolPart) => boolean;
};

export type ThreadProps = {
  components: ThreadComponents;
  // commands — slash-команды агента для автокомплита в composer'е (см. SlashMenu).
  commands?: AvailableCommand[] | undefined;
  // footer — дополнительный блок над composer'ом (например, план агента).
  footer?: ReactNode | undefined;
  // composer — замена обычного ввода для другого ожидаемого действия, например
  // ответа на permission-запрос агента.
  composer?: ReactNode | undefined;
  // configOptions — конфигурационные опции сессии (модель, усилие) для селекторов
  // в composer'е; onConfigChange отправляет новое значение бэкенду.
  configOptions?: ConfigOption[] | undefined;
  onConfigChange?: ((configId: string, value: string) => void) | undefined;
  responseProfiles?: { id: string; name: string; deleted?: boolean }[] | undefined;
  responseProfileId?: string | undefined;
  responseProfileBusy?: boolean | undefined;
  onResponseProfileChange?: ((id: string) => void) | undefined;
  // readonly — режим только для чтения (архивная сессия): скрывает composer и
  // suggestions, лента остаётся. Ввод недоступен — живого агента нет.
  readonly?: boolean | undefined;
  // workspace — компактная лента под постоянным предметным интерфейсом плагина.
  workspace?: boolean | undefined;
};

const ThreadComponentsContext = createContext<ThreadComponents | null>(null);

// useThreadComponents — слоты рендера ленты. Провайдер ставит Thread, поэтому вне его
// компоненты ленты не рендерятся.
function useThreadComponents(): ThreadComponents {
  const ctx = useContext(ThreadComponentsContext);
  if (!ctx) throw new Error("Thread components context is missing");
  return ctx;
}

// Контекст списка slash-команд: проброшен от Thread до вложенного Composer без
// передачи пропом через все промежуточные компоненты registry-разметки.
const CommandsContext = createContext<AvailableCommand[]>([]);

// Контекст конфигурационных опций сессии для селекторов composer'а (см. ConfigSelectors).
type ConfigContextValue = {
  options: ConfigOption[];
  onChange: (configId: string, value: string) => void;
};
const ConfigContext = createContext<ConfigContextValue>({
  options: [],
  onChange: () => {},
});

type ResponseProfileContextValue = {
  profiles: { id: string; name: string; deleted?: boolean }[];
  selected: string;
  busy: boolean;
  onChange: (id: string) => void;
};
const ResponseProfileContext = createContext<ResponseProfileContextValue>({
  profiles: [],
  selected: "default",
  busy: false,
  onChange: () => {},
});

// Startup exposes a loading placeholder thread; treat it as a new chat so
// the composer mounts centered. Loads after startup keep the docked layout.
const isNewChatView = (s: AssistantState) =>
  s.thread.messages.length === 0 &&
  (!s.thread.isLoading || s.threads.isLoading);

export const Thread: FC<ThreadProps> = ({
  components,
  commands = [],
  footer,
  composer,
  configOptions = [],
  onConfigChange,
  responseProfiles = [],
  responseProfileId = "default",
  responseProfileBusy = false,
  onResponseProfileChange,
  readonly = false,
  workspace = false,
}) => {
  const isEmpty = useAuiState(isNewChatView);
  const configValue = useMemo<ConfigContextValue>(
    () => ({ options: configOptions, onChange: onConfigChange ?? (() => {}) }),
    [configOptions, onConfigChange],
  );
  const profileValue = useMemo<ResponseProfileContextValue>(
    () => ({
      profiles: responseProfiles,
      selected: responseProfileId,
      busy: responseProfileBusy,
      onChange: onResponseProfileChange ?? (() => {}),
    }),
    [responseProfiles, responseProfileId, responseProfileBusy, onResponseProfileChange],
  );

  return (
    <ThreadComponentsContext.Provider value={components}>
      <CommandsContext.Provider value={commands}>
        <ConfigContext.Provider value={configValue}>
          <ResponseProfileContext.Provider value={profileValue}>
            <ThreadRoot
              isEmpty={isEmpty}
              footer={footer}
              composer={composer}
              readonly={readonly}
              workspace={workspace}
            />
          </ResponseProfileContext.Provider>
        </ConfigContext.Provider>
      </CommandsContext.Provider>
    </ThreadComponentsContext.Provider>
  );
};

const ThreadRoot: FC<{
  isEmpty: boolean;
  footer?: ReactNode;
  composer?: ReactNode;
  readonly?: boolean;
  workspace?: boolean;
}> = ({ isEmpty, footer, composer, readonly = false, workspace = false }) => {
  return (
    <ThreadPrimitive.Root
      className="aui-root aui-thread-root bg-background @container flex h-full flex-col"
      style={{
        ["--thread-max-width" as string]: "45rem",
        ["--composer-bg" as string]:
          "color-mix(in oklab, var(--color-muted) 30%, var(--color-background))",
        ["--composer-radius" as string]: "1.5rem",
        ["--composer-padding" as string]: "8px",
      }}
    >
      {/* Без turnAnchor="top" (дефолт registry): он прокручивал начало нового
          turn'а к верху вьюпорта — «отскролл наверх» при каждом сообщении.
          Обычное поведение — прилипание к низу, как в терминале. */}
      <ThreadPrimitive.Viewport
        data-slot="aui_thread-viewport"
        className="relative flex flex-1 flex-col overflow-x-auto overflow-y-scroll scroll-smooth"
      >
        <div
          className={cn(
            // pt-14 — запас под плавающие чипы управления окнами (см. features/acp/dock).
            "mx-auto flex w-full max-w-(--thread-max-width) flex-1 flex-col px-4",
            workspace ? "pt-3" : "pt-14",
            isEmpty && !workspace && "justify-center",
          )}
        >
          {!workspace && (
            <AuiIf condition={isNewChatView}>
              <ThreadWelcome />
            </AuiIf>
          )}

          <div
            data-slot="aui_message-group"
            className={cn("flex flex-col empty:hidden", workspace ? "mb-3 gap-y-3" : "mb-14 gap-y-6")}
          >
            <ThreadPrimitive.Messages>
              {() => <ThreadMessage />}
            </ThreadPrimitive.Messages>
          </div>

          <ThreadPrimitive.ViewportFooter
            className={cn(
              "aui-thread-viewport-footer bg-background flex flex-col overflow-visible",
              workspace ? "gap-2 pb-3" : "gap-4 pb-4 md:pb-6",
              !isEmpty &&
                "sticky bottom-0 mt-auto rounded-t-(--composer-radius)",
            )}
          >
            <ThreadScrollToBottom />
            {footer}
            {!readonly && (composer ?? <Composer />)}
          </ThreadPrimitive.ViewportFooter>
        </div>
      </ThreadPrimitive.Viewport>
    </ThreadPrimitive.Root>
  );
};

const ThreadMessage: FC = () => {
  const role = useAuiState((s) => s.message.role);
  const isEditing = useAuiState((s) => s.message.composer.isEditing);

  if (isEditing) return <EditComposer />;
  if (role === "user") return <UserMessage />;
  // Системные уведомления (wake-up харнесса о фоновых задачах) бэкенд транслирует
  // role=system — компактная информационная карточка, не реплика диалога.
  if (role === "system") return <SystemMessage />;
  return <AssistantMessage />;
};

// SystemMessage — компактная системная карточка по центру ленты: уведомление о фоновой
// задаче (завершилась/упала), инжектированное харнессом агента между turn'ами.
const SystemMessage: FC = () => {
  return (
    <MessagePrimitive.Root className="aui-system-message mx-auto flex w-full max-w-[var(--thread-max-width)] justify-center py-1">
      <div className="bg-muted/60 text-muted-foreground flex max-w-[85%] items-start gap-2 rounded-lg border border-dashed px-3 py-2 text-xs">
        <InfoIcon className="mt-0.5 size-3.5 shrink-0 opacity-70" />
        <span className="min-w-0 break-words">
          <MessagePrimitive.Parts />
        </span>
      </div>
    </MessagePrimitive.Root>
  );
};

const ThreadScrollToBottom: FC = () => {
  return (
    <ThreadPrimitive.ScrollToBottom asChild>
      <TooltipIconButton
        tooltip="Scroll to bottom"
        variant="outline"
        className="aui-thread-scroll-to-bottom dark:border-border dark:bg-background dark:hover:bg-accent absolute -top-12 z-10 self-center rounded-full p-4 disabled:invisible"
      >
        <ArrowDownIcon />
      </TooltipIconButton>
    </ThreadPrimitive.ScrollToBottom>
  );
};

const ThreadWelcome: FC = () => {
  return (
    <div className="aui-thread-welcome-root mb-6 flex flex-col items-center px-4 text-center">
      <h1 className="aui-thread-welcome-message-inner fade-in slide-in-from-bottom-1 animate-in fill-mode-both text-2xl font-semibold duration-200">
        How can I help you today?
      </h1>
    </div>
  );
};

const Composer: FC = () => {
  // Ввод заблокирован, пока грузится история треда: отправка первого сообщения до
  // завершения load() попадала бы под перезапись applyExternalMessages (загруженная
  // история затирает живую пару user/ответ, и стрим уезжает в чужое сообщение).
  const historyLoading = useAuiState((s) => s.thread.isLoading);

  return (
    <ComposerPrimitive.Root className="aui-composer-root relative flex w-full flex-col">
      <div
        data-slot="aui_composer-shell"
        className="border-border/60 focus-within:border-border dark:border-muted-foreground/15 dark:focus-within:border-muted-foreground/30 relative flex w-full flex-col gap-2 rounded-(--composer-radius) border bg-(--composer-bg) p-(--composer-padding) shadow-[0_4px_16px_-8px_rgba(0,0,0,0.08),0_1px_2px_rgba(0,0,0,0.04)] transition-[border-color,box-shadow] focus-within:shadow-[0_6px_24px_-8px_rgba(0,0,0,0.12),0_1px_2px_rgba(0,0,0,0.05)] dark:shadow-none"
      >
        <ComposerContextZone />
        <SlashMenu />
        <ComposerInput historyLoading={historyLoading} />
          <ComposerAction />
        </div>
    </ComposerPrimitive.Root>
  );
};

// isCoarsePointer — тач-устройство (мобильный браузер): там Enter вставляет перенос строки,
// а отправка — кнопкой, поэтому Enter-перехват контекста на нём не включаем.
function isCoarsePointer(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(pointer: coarse)").matches
  );
}

// ComposerInput — поле ввода с перехватом Enter: когда в зоне контекста есть элементы,
// desktop-Enter отправляет через sendWithContext (дописывает контекст к промпту), иначе —
// нативная отправка примитива. Перехват в capture-фазе, чтобы опередить встроенный Enter→send.
const ComposerInput: FC<{ historyLoading: boolean }> = ({ historyLoading }) => {
  const { hasItems, sendWithContext } = useComposerContextSend();
  const onKeyDownCapture = (e: ReactKeyboardEvent) => {
    if (e.key !== "Enter" || e.nativeEvent.isComposing || isCoarsePointer()) return;
    const mode = getComposerSubmitMode();
    if (!shouldSubmitComposer(mode, e.nativeEvent)) {
      if (mode === "modifier-enter") {
        // assistant-ui отправляет plain Enter сам; останавливаем bubbling, сохраняя
        // нативную вставку переноса строки в textarea.
        e.stopPropagation();
      }
      return;
    }
    if (hasItems || mode === "modifier-enter") {
      e.preventDefault();
      e.stopPropagation();
      sendWithContext();
    }
  };
  return (
    <ComposerPrimitive.Input
      placeholder={
        historyLoading ? "Загрузка истории…" : "Сообщение агенту…  (/ — команды)"
      }
      disabled={historyLoading}
      className="aui-composer-input placeholder:text-muted-foreground/80 max-h-32 min-h-10 w-full resize-none bg-transparent px-2.5 py-1 text-base outline-none disabled:opacity-60"
      rows={1}
      autoFocus
      // На тач-устройствах (мобильный браузер) нет Shift → Shift+Enter недоступен. Проп
      // заставляет plain Enter вставлять \n на touch-primary устройствах; отправка — кнопкой.
      // На десктопе Enter отправляет.
      unstable_insertNewlineOnTouchEnter
      aria-label="Message input"
      onKeyDownCapture={onKeyDownCapture}
    />
  );
};

// slashQuery возвращает текст после ведущего «/», если ввод — начало slash-команды
// (одно слово со «/», без пробелов). Иначе null: «/» в середине текста меню не триггерит.
function slashQuery(text: string): string | null {
  if (!text.startsWith("/")) return null;
  const rest = text.slice(1);
  if (/\s/.test(rest)) return null;
  return rest;
}

// SlashMenu — выпадающее меню slash-команд агента над полем ввода. Открывается, когда
// текст composer'а начинается со «/», фильтрует команды по подстроке и подставляет
// выбранную. Навигация — стрелки/Enter/Tab, закрытие — Esc. Список команд приходит
// CUSTOM-событием available_commands (см. useAcpRuntime) через CommandsContext.
const SlashMenu: FC = () => {
  const commands = useContext(CommandsContext);
  const composer = useComposerRuntime();
  const text = useComposer((c) => c.text);
  const [active, setActive] = useState(0);
  const dismissedFor = useRef<string | null>(null);

  const query = slashQuery(text);
  const matches = useMemo(() => {
    if (query === null || commands.length === 0) return [];
    const q = query.toLowerCase();
    return commands.filter((c) => c.name.toLowerCase().includes(q)).slice(0, 8);
  }, [query, commands]);

  const open =
    query !== null && matches.length > 0 && dismissedFor.current !== text;

  useEffect(() => {
    setActive(0);
  }, [query, matches.length]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();
        setActive((i) => (i + 1) % matches.length);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        e.stopPropagation();
        setActive((i) => (i - 1 + matches.length) % matches.length);
      } else if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        e.stopPropagation();
        choose(matches[active]);
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        dismissedFor.current = text;
        setActive(0);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [open, matches, active, text]);

  function choose(cmd: AvailableCommand) {
    composer.setText(`/${cmd.name} `);
    dismissedFor.current = null;
  }

  if (!open) return null;

  return (
    <div className="absolute bottom-[calc(100%+0.5rem)] left-0 z-10 w-full max-w-md overflow-hidden rounded-xl border bg-popover shadow-md">
      <ul className="max-h-64 overflow-y-auto py-1">
        {matches.map((cmd, i) => (
          <li key={cmd.name}>
            <button
              type="button"
              onMouseDown={(e) => {
                e.preventDefault();
                choose(cmd);
              }}
              onMouseEnter={() => setActive(i)}
              className={cn(
                "flex w-full flex-col items-start gap-0.5 px-3 py-1.5 text-left",
                i === active ? "bg-accent" : "hover:bg-accent/50",
              )}
            >
              <span className="font-mono text-sm">
                /{cmd.name}
                {cmd.hint && (
                  <span className="ml-1 text-muted-foreground">{cmd.hint}</span>
                )}
              </span>
              {cmd.description && (
                <span className="line-clamp-1 text-xs text-muted-foreground">
                  {cmd.description}
                </span>
              )}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
};

// configSelectorCategories — какие опции сессии показываются селекторами в composer'е
// и в каком порядке. Категория "mode" — режим прав/агента (напр. auto-approve): даёт
// выбрать авто-одобрение инструментов вместо ручного подтверждения каждого запроса.
const configSelectorCategories = ["model", "mode", "thought_level"];

// ConfigSelectors — компактные выпадающие селекторы опций сессии (модель, усилие)
// слева в строке действий composer'а. Текущее значение — подписью на кнопке; выбор
// уходит бэкенду (session/set_config_option) через onConfigChange.
const ConfigSelectors: FC = () => {
  const { options, onChange } = useContext(ConfigContext);
  const shown = configSelectorCategories
    .map((cat) => options.find((o) => o.category === cat))
    .filter((o): o is ConfigOption => o !== undefined && o.options.length > 0);
  if (shown.length === 0) return null;

  return (
    <div className="flex items-center gap-0.5">
      {shown.map((opt) => {
        const current = opt.options.find((v) => v.value === opt.currentValue);
        return (
          <DropdownMenu key={opt.id}>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-muted-foreground hover:text-foreground h-7 shrink-0 gap-1 px-2 text-xs font-normal"
              >
                {/* Префикс названием опции: несколько селекторов могут иметь
                    одинаковое текущее значение (напр. Mode=Default и Effort=Default),
                    без подписи их не различить. */}
                <span className="opacity-60">{opt.name}:</span>
                {current?.name ?? opt.currentValue}
                <ChevronDownIcon className="size-3 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent side="top" align="start" className="w-64">
              {opt.options.map((v) => (
                <DropdownMenuItem
                  key={v.value}
                  onSelect={() => onChange(opt.id, v.value)}
                  className="flex-col items-start gap-0.5"
                >
                  <span className="flex w-full items-center justify-between text-sm">
                    {v.name}
                    {v.value === opt.currentValue && (
                      <CheckIcon className="size-3.5" />
                    )}
                  </span>
                  {v.description && (
                    <span className="text-muted-foreground text-xs">
                      {v.description}
                    </span>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        );
      })}
    </div>
  );
};

const ResponseProfileSelector: FC = () => {
  const { profiles, selected, busy, onChange } = useContext(ResponseProfileContext);
  const running = useAuiState((s) => s.thread.isRunning);
  if (profiles.length === 0) return null;
  const current = profiles.find((profile) => profile.id === selected);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={busy || running}
          className="text-muted-foreground hover:text-foreground h-7 shrink-0 gap-1 px-2 text-xs font-normal"
        >
          <span className="opacity-60">Ответы:</span>
          {current?.name ?? "Обычно"}
          {busy ? <Loader2Icon className="size-3 animate-spin" /> : <ChevronDownIcon className="size-3 opacity-60" />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start" className="w-56">
        {profiles.map((profile) => (
          <DropdownMenuItem
            key={profile.id}
            disabled={profile.deleted}
            onSelect={() => onChange(profile.id)}
          >
            <span className="flex w-full items-center justify-between">
              {profile.name}{profile.deleted ? " · удалён" : ""}
              {profile.id === selected && <CheckIcon className="size-3.5" />}
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
};

// SendButton — кнопка отправки. При непустой зоне контекста шлёт через sendWithContext
// (дописывает контекст к промпту), иначе — нативная ComposerPrimitive.Send (с её disabled-
// логикой на пустой ввод). AuiIf в ComposerAction рендерит её только когда не идёт генерация.
const SendButton: FC = () => {
  const { hasItems, sendWithContext } = useComposerContextSend();
  const icon = <ArrowUpIcon className="aui-composer-send-icon size-4.5" />;
  if (!hasItems) {
    return (
      <ComposerPrimitive.Send asChild>
        <TooltipIconButton
          tooltip="Send message"
          side="bottom"
          type="button"
          variant="default"
          size="icon"
          className="aui-composer-send size-7 rounded-full"
          aria-label="Send message"
        >
          {icon}
        </TooltipIconButton>
      </ComposerPrimitive.Send>
    );
  }
  return (
    <TooltipIconButton
      tooltip="Send message"
      side="bottom"
      type="button"
      variant="default"
      size="icon"
      className="aui-composer-send size-7 rounded-full"
      aria-label="Send message"
      onClick={sendWithContext}
    >
      {icon}
    </TooltipIconButton>
  );
};

const ComposerAction: FC = () => {
  return (
    <div className="aui-composer-action-wrapper relative flex items-center justify-between gap-1">
      {/* min-w-0 + overflow-x-auto: на узких экранах (мобильный браузер) селекторы
          опций шире вьюпорта — группа сжимается и скроллится внутри себя, не выталкивая
          кнопку отправки за край экрана (горизонтальный скролл страницы). */}
      <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        <ComposerUploadButton />
        <ResponseProfileSelector />
        <ConfigSelectors />
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <AuiIf condition={(s) => s.thread.capabilities.dictation}>
          <AuiIf condition={(s) => s.composer.dictation == null}>
            <ComposerPrimitive.Dictate asChild>
              <TooltipIconButton
                tooltip="Voice input"
                side="bottom"
                type="button"
                variant="ghost"
                size="icon"
                className="aui-composer-dictate size-7 rounded-full"
                aria-label="Start voice input"
              >
                <MicIcon className="aui-composer-dictate-icon size-4" />
              </TooltipIconButton>
            </ComposerPrimitive.Dictate>
          </AuiIf>
          <AuiIf condition={(s) => s.composer.dictation != null}>
            <ComposerPrimitive.StopDictation asChild>
              <TooltipIconButton
                tooltip="Stop dictation"
                side="bottom"
                type="button"
                variant="ghost"
                size="icon"
                className="aui-composer-stop-dictation text-destructive size-7 rounded-full"
                aria-label="Stop voice input"
              >
                <SquareIcon className="aui-composer-stop-dictation-icon size-3.5 animate-pulse fill-current" />
              </TooltipIconButton>
            </ComposerPrimitive.StopDictation>
          </AuiIf>
        </AuiIf>
        <AuiIf condition={(s) => !s.thread.isRunning}>
          <SendButton />
        </AuiIf>
        <AuiIf condition={(s) => s.thread.isRunning}>
          <ComposerPrimitive.Cancel asChild>
            <Button
              type="button"
              variant="default"
              size="icon"
              className="aui-composer-cancel size-7 rounded-full"
              aria-label="Stop generating"
            >
              <SquareIcon className="aui-composer-cancel-icon size-3.5 fill-current" />
            </Button>
          </ComposerPrimitive.Cancel>
        </AuiIf>
      </div>
    </div>
  );
};

const MessageError: FC = () => {
  return (
    <MessagePrimitive.Error>
      <ErrorPrimitive.Root className="aui-message-error-root border-destructive bg-destructive/10 text-destructive dark:bg-destructive/5 mt-2 rounded-md border p-3 text-sm dark:text-red-200">
        <ErrorPrimitive.Message className="aui-message-error-message line-clamp-2" />
      </ErrorPrimitive.Root>
    </MessagePrimitive.Error>
  );
};

const AssistantMessage: FC = () => {
  const { ToolFallback, ToolGroup, isToolStandalone } = useThreadComponents();
  const messageGroupBy = useMemo(() => {
    if (!isToolStandalone) return defaultMessageGroupBy;
    return (
      part: Parameters<typeof defaultMessageGroupBy>[0],
      context: Parameters<typeof defaultMessageGroupBy>[1],
    ) => {
      if (part.type === "tool-call" && isToolStandalone(part)) return [];
      return defaultMessageGroupBy(part, context);
    };
  }, [isToolStandalone]);
  // navId — якорь для прыжка по шкале навигации и панели ссылок (см. dock/jumpToMessage).
  const navId = useAuiState((s) => s.message.id);

  // reserves space for action bar and compensates with `-mb` for consistent msg spacing
  // keeps hovered action bar from shifting layout (autohide doesn't support absolute positioning well)
  // for pt-[n] use -mb-[n + 6] & min-h-[n + 6] to preserve compensation
  const ACTION_BAR_PT = "pt-1.5";
  const ACTION_BAR_HEIGHT = `-mb-7.5 min-h-7.5 ${ACTION_BAR_PT}`;

  return (
    <MessagePrimitive.Root
      data-slot="aui_assistant-message-root"
      data-role="assistant"
      data-nav-id={navId}
      className="fade-in slide-in-from-bottom-1 animate-in relative scroll-mt-5 duration-150"
    >
      <div
        data-slot="aui_assistant-message-content"
        // [contain-intrinsic-size:auto_24px] fixes issue #4104, don't change without checking for regressions
        className="text-foreground px-2 leading-relaxed wrap-break-word [contain-intrinsic-size:auto_24px] [content-visibility:auto]"
      >
        <MessagePrimitive.GroupedParts
          groupBy={messageGroupBy}
        >
          {({ part, children }) => {
            switch (part.type) {
              case "group-chainOfThought":
                return <div data-slot="aui_chain-of-thought">{children}</div>;
              case "group-tool":
                return <ToolGroup group={part}>{children}</ToolGroup>;
              case "group-reasoning": {
                const running = part.status.type === "running";
                return (
                  <ReasoningRoot streaming={running}>
                    <ReasoningTrigger active={running} />
                    <ReasoningContent aria-busy={running}>
                      <ReasoningText>{children}</ReasoningText>
                    </ReasoningContent>
                  </ReasoningRoot>
                );
              }
              case "text":
                // Ответ агента набран серифом — фирменная черта Claude. Серифом идёт
                // только сам текст: карточки инструментов, размышления и action-bar
                // остаются в UI-шрифте, код внутри markdown — моноширинным.
                return <AssistantMessageText />;
              case "reasoning":
                return <Reasoning {...part} />;
              case "tool-call":
                return part.toolUI ?? <ToolFallback {...part} />;
              case "data":
                return part.dataRendererUI;
              case "indicator":
                return (
                  <span
                    data-slot="aui_assistant-message-indicator"
                    className="animate-pulse font-sans"
                    aria-label="Assistant is working"
                  >
                    {"●"}
                  </span>
                );
              default:
                return null;
            }
          }}
        </MessagePrimitive.GroupedParts>
        <MessageError />
      </div>

      <div
        data-slot="aui_assistant-message-footer"
        className={cn("ms-2 flex items-center", ACTION_BAR_HEIGHT)}
      >
        <AssistantActionBar />
      </div>
    </MessagePrimitive.Root>
  );
};

const AssistantMessageText: FC = () => {
  const { text, status } = useMessagePartText();
  const contentRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useLayoutEffect(() => {
    const element = contentRef.current;
    if (!element || status.type !== "complete") {
      setOverflowing(false);
      return;
    }
    if (expanded) return;
    const update = () => setOverflowing(element.scrollHeight > element.clientHeight);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, [expanded, status.type, text]);

  return (
    <div className="font-serif text-[16px] leading-[1.72]">
      <div
        ref={contentRef}
        className={cn(!expanded && status.type === "complete" && "max-h-[80rem] overflow-hidden")}
      >
        <MarkdownText />
      </div>
      {overflowing && (
        <button
          type="button"
          className="text-primary mt-2 font-sans text-xs font-medium hover:underline"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? "Свернуть" : "Развернуть ответ"}
        </button>
      )}
    </div>
  );
};

const AssistantActionBar: FC = () => {
  return (
    <ActionBarPrimitive.Root
      hideWhenRunning
      autohide="not-last"
      className="aui-assistant-action-bar-root text-muted-foreground animate-in fade-in col-start-3 row-start-2 -ms-1 flex gap-1 duration-200"
    >
      <ActionBarPrimitive.Copy asChild>
        <TooltipIconButton tooltip="Copy">
          <AuiIf condition={(s) => s.message.isCopied}>
            <CheckIcon className="animate-in zoom-in-50 fade-in duration-200 ease-out" />
          </AuiIf>
          <AuiIf condition={(s) => !s.message.isCopied}>
            <CopyIcon className="animate-in zoom-in-75 fade-in duration-150" />
          </AuiIf>
        </TooltipIconButton>
      </ActionBarPrimitive.Copy>
      <ActionBarMorePrimitive.Root>
        <ActionBarMorePrimitive.Trigger asChild>
          <TooltipIconButton
            tooltip="More"
            className="data-[state=open]:bg-accent"
          >
            <MoreHorizontalIcon />
          </TooltipIconButton>
        </ActionBarMorePrimitive.Trigger>
        <ActionBarMorePrimitive.Content
          side="bottom"
          align="start"
          sideOffset={6}
          className="aui-action-bar-more-content bg-popover/95 text-popover-foreground data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=open]:animate-in data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=closed]:animate-out data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 z-50 min-w-[8rem] overflow-hidden rounded-xl border p-1.5 shadow-lg backdrop-blur-sm"
        >
          <ActionBarPrimitive.ExportMarkdown asChild>
            <ActionBarMorePrimitive.Item className="aui-action-bar-more-item hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground flex cursor-pointer items-center gap-2 rounded-lg px-2.5 py-1.5 text-sm outline-none select-none">
              <DownloadIcon className="size-4" />
              Export as Markdown
            </ActionBarMorePrimitive.Item>
          </ActionBarPrimitive.ExportMarkdown>
        </ActionBarMorePrimitive.Content>
      </ActionBarMorePrimitive.Root>
    </ActionBarPrimitive.Root>
  );
};

const UserMessage: FC = () => {
  // navId — якорь для прыжка по шкале навигации (см. dock/jumpToMessage).
  const navId = useAuiState((s) => s.message.id);

  return (
    <MessagePrimitive.Root
      data-slot="aui_user-message-root"
      className="fade-in slide-in-from-bottom-1 animate-in grid auto-rows-auto grid-cols-[minmax(72px,1fr)_auto] content-start gap-y-2 scroll-mt-5 px-2 duration-150 [contain-intrinsic-size:auto_60px] [content-visibility:auto] [&:where(>*)]:col-start-2"
      data-role="user"
      data-nav-id={navId}
    >
      <UserMessageAttachments />

      <div className="aui-user-message-content-wrapper relative col-start-2 min-w-0">
        <div className="aui-user-message-content peer bg-secondary text-foreground border-border rounded-[16px_16px_4px_16px] border px-4 py-3 text-[14px] leading-[1.55] wrap-break-word empty:hidden">
          <MessagePrimitive.Parts components={{ Text: UserMessageText }} />
        </div>
      </div>
    </MessagePrimitive.Root>
  );
};

const URL_PATTERN = /https?:\/\/[^\s<]+/gi;
const TRAILING_URL_PUNCTUATION = /[.,!?;:)}\]]+$/;

const UserMessageText: FC = () => {
  const { text, status } = useMessagePartText();
  const contentRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useLayoutEffect(() => {
    const element = contentRef.current;
    if (!element || expanded) return;
    const update = () => setOverflowing(element.scrollHeight > element.clientHeight);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, [expanded, text]);

  const parts: ReactNode[] = [];
  let offset = 0;
  for (const match of text.matchAll(URL_PATTERN)) {
    const index = match.index;
    const rawURL = match[0];
    const url = rawURL.replace(TRAILING_URL_PUNCTUATION, "");
    parts.push(text.slice(offset, index));
    parts.push(
      <LinkWithPreview key={`${index}-${url}`} href={url}>
        {url}
      </LinkWithPreview>,
    );
    parts.push(rawURL.slice(url.length));
    offset = index + rawURL.length;
  }
  parts.push(text.slice(offset));

  return (
    <div data-status={status.type}>
      <div
        ref={contentRef}
        className={cn(
          "whitespace-pre-wrap break-words",
          !expanded && "max-h-40 overflow-hidden",
        )}
      >
        {parts}
      </div>
      {overflowing && (
        <button
          type="button"
          className="text-primary mt-2 text-xs font-medium hover:underline"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? "Свернуть" : "Развернуть"}
        </button>
      )}
    </div>
  );
};

const EditComposer: FC = () => {
  return (
    <MessagePrimitive.Root
      data-slot="aui_edit-composer-wrapper"
      className="flex flex-col px-2"
    >
      <ComposerPrimitive.Root className="aui-edit-composer-root border-border/60 dark:border-muted-foreground/15 ms-auto flex w-full max-w-[85%] flex-col rounded-(--composer-radius) border bg-(--composer-bg) shadow-[0_4px_16px_-8px_rgba(0,0,0,0.08),0_1px_2px_rgba(0,0,0,0.04)] dark:shadow-none">
        <ComposerPrimitive.Input
          className="aui-edit-composer-input text-foreground min-h-14 w-full resize-none bg-transparent px-4 pt-3 pb-1 text-base outline-none"
          autoFocus
        />
        <div className="aui-edit-composer-footer mx-2.5 mb-2.5 flex items-center gap-1.5 self-end">
          <ComposerPrimitive.Cancel asChild>
            <Button
              variant="ghost"
              size="sm"
              className="h-8 rounded-full px-3.5"
            >
              Cancel
            </Button>
          </ComposerPrimitive.Cancel>
          <ComposerPrimitive.Send asChild>
            <Button size="sm" className="h-8 rounded-full px-3.5">
              Update
            </Button>
          </ComposerPrimitive.Send>
        </div>
      </ComposerPrimitive.Root>
    </MessagePrimitive.Root>
  );
};
