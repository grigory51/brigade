import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useComposerRuntime, useThreadRuntime } from "@assistant-ui/react";
import { Quote, Paperclip, X } from "lucide-react";

// PendingContext — «зона контекста» текущего ACP-треда над инпутом: накопленные фрагменты
// (цитаты из выделения — действие «Спросить») и приложенные файлы. При отправке блок контекста
// дописывается префиксом к тексту промпта (транспорт AG-UI текстовый: файлы уходят своим путём
// uploads/<...>, агент читает их сам; цитаты — процитированными блоками). Прокидывается
// React-контекстом по образцу ComposerUploadContext: null → зоны нет (readonly-архив).

export type ContextItem =
  | { id: string; kind: "quote"; text: string }
  | { id: string; kind: "file"; path: string; name: string };

type PendingContextValue = {
  items: ContextItem[];
  add: (item: Omit<Extract<ContextItem, { kind: "quote" }>, "id"> | Omit<Extract<ContextItem, { kind: "file" }>, "id">) => void;
  remove: (id: string) => void;
  clear: () => void;
};

const PendingContext = createContext<PendingContextValue | null>(null);

export function usePendingContext(): PendingContextValue | null {
  return useContext(PendingContext);
}

export function PendingContextProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ContextItem[]>([]);
  const value = useMemo<PendingContextValue>(
    () => ({
      items,
      add: (item) =>
        setItems((prev) => [...prev, { ...item, id: crypto.randomUUID() } as ContextItem]),
      remove: (id) => setItems((prev) => prev.filter((i) => i.id !== id)),
      clear: () => setItems([]),
    }),
    [items],
  );
  return <PendingContext.Provider value={value}>{children}</PendingContext.Provider>;
}

// buildContextBlock собирает текстовый блок контекста. Пусто — если нет элементов. Блок
// дописывается ПОСЛЕ текста пользователя (не перед): иначе slash-команда (`/note …`) оказалась
// бы не в начале сообщения и не сработала бы.
export function buildContextBlock(items: ContextItem[]): string {
  if (items.length === 0) return "";
  const lines: string[] = ["Контекст:"];
  for (const it of items) {
    if (it.kind === "quote") lines.push(`> «${it.text.trim()}»`);
  }
  const files = items.filter((i) => i.kind === "file").map((i) => i.path);
  if (files.length > 0) lines.push(`Приложенные файлы: ${files.join(", ")}`);
  return lines.join("\n");
}

// useComposerContextSend — отправка с подмешиванием контекста. hasItems=false → вызывающий
// использует нативную отправку. sendWithContext дописывает префикс к тексту композера и шлёт
// новой user-репликой (append запускает прогон, как A2UI-действия), затем чистит инпут и зону.
export function useComposerContextSend(): {
  hasItems: boolean;
  sendWithContext: () => void;
} {
  const ctx = usePendingContext();
  const thread = useThreadRuntime();
  const composer = useComposerRuntime();
  const sendWithContext = useCallback(() => {
    const items = ctx?.items ?? [];
    const text = composer.getState().text;
    const block = buildContextBlock(items);
    const full = text.trim() ? (block ? `${text}\n\n${block}` : text) : block;
    if (!full.trim()) return;
    void thread.append({ role: "user", content: [{ type: "text", text: full }] });
    composer.setText("");
    ctx?.clear();
  }, [ctx, thread, composer]);
  return { hasItems: (ctx?.items.length ?? 0) > 0, sendWithContext };
}

// ComposerContextZone — ряд чипов над инпутом: цитаты (обрезанный текст) и файлы (имя), каждый
// с крестиком удаления. Пусто/нет провайдера → ничего не рендерит.
export function ComposerContextZone() {
  const ctx = usePendingContext();
  if (!ctx || ctx.items.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5 px-1 pb-1">
      {ctx.items.map((it) => (
        <span
          key={it.id}
          className="inline-flex max-w-[16rem] items-center gap-1.5 rounded-md border bg-muted/60 px-2 py-1 text-xs text-muted-foreground"
        >
          {it.kind === "quote" ? (
            <Quote className="size-3 shrink-0" />
          ) : (
            <Paperclip className="size-3 shrink-0" />
          )}
          <span className="truncate">
            {it.kind === "quote" ? it.text.trim() : it.name}
          </span>
          <button
            type="button"
            onClick={() => ctx.remove(it.id)}
            aria-label="Убрать из контекста"
            className="shrink-0 cursor-pointer rounded-sm text-muted-foreground/70 hover:text-foreground"
          >
            <X className="size-3" />
          </button>
        </span>
      ))}
    </div>
  );
}
