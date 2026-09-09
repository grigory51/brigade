import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Code, ConnectError } from "@connectrpc/connect";
import { ArrowRight, BookOpenText, FolderPlus, Loader2, Plus, RefreshCw, Search, SearchX, Sparkles, Unplug } from "lucide-react";
import { toast } from "sonner";

import { memoryClient } from "@/api/client";
import type { Topic } from "@/api/gen/brigade/v1/memory_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/empty-state";
import { cn } from "@/lib/utils";
import {
  noteCountLabel,
  noteType,
  plural,
  softColor,
  TOPIC_COLORS,
} from "./notes";

// MemoryPage — «полки тем»: обзор всех тем памяти, вход в любую. Тема — главный герой:
// карточка показывает обзор-синтез и пару последних заметок. Плоского списка заметок больше
// нет — заметка всегда живёт внутри темы.
export function MemoryPage() {
  const [topics, setTopics] = useState<Topic[] | null>(null);
  const [query, setQuery] = useState("");
  const [composerOpen, setComposerOpen] = useState(false);
  // configured=false — у пользователя не настроен git-репозиторий памяти.
  const [configured, setConfigured] = useState(true);
  const [loadError, setLoadError] = useState("");
  // syncing — идёт ручной pull с origin (кнопка «Обновить»).
  const [syncing, setSyncing] = useState(false);

  const load = useCallback(async () => {
    setLoadError("");
    try {
      const r = await memoryClient.listTopics({ query: "" });
      setTopics(r.topics);
      setConfigured(true);
    } catch (err) {
      setTopics([]);
      if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
        setConfigured(false);
        return;
      }
      setLoadError(
        err instanceof ConnectError ? err.rawMessage : "Не удалось загрузить темы",
      );
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // sync — подтянуть изменения памяти с origin (правки из другого инстанса brigade), затем
  // перечитать темы. У каждого инстанса свой локальный клон, авто-pull нет — только по кнопке.
  const sync = useCallback(async () => {
    setSyncing(true);
    try {
      await memoryClient.syncMemory({});
      await load();
      toast.success("Заметки обновлены");
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось обновить заметки",
      );
    } finally {
      setSyncing(false);
    }
  }, [load]);

  const filtered = useMemo(() => {
    if (!topics) return [];
    const q = query.trim().toLowerCase();
    if (!q) return topics;
    return topics.filter(
      (t) =>
        t.name.toLowerCase().includes(q) ||
        t.synthesis.toLowerCase().includes(q) ||
        t.recent.some(
          (n) =>
            n.title.toLowerCase().includes(q) || n.body.toLowerCase().includes(q),
        ),
    );
  }, [topics, query]);

  if (topics === null) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Loader2 className="size-5 animate-spin" />
      </div>
    );
  }

  if (!configured || loadError) {
    return (
      <div className="flex h-full flex-col">
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
          {loadError ? (
            <EmptyState icon={Unplug} title="Не удалось открыть заметки" description={loadError}>
              <Button onClick={() => { setTopics(null); void load(); }}>
                <RefreshCw className="size-4" />Попробовать снова
              </Button>
            </EmptyState>
          ) : (
            <EmptyState
              icon={BookOpenText}
              title="Важное остаётся с вами"
              description="Сохраняйте решения и идеи из разговоров в заметки, доступные во всех сессиях. Подключите приватный git-репозиторий, чтобы начать."
            >
              <Button asChild>
                <Link to="/settings/memory">Настроить заметки<ArrowRight className="size-4" /></Link>
              </Button>
            </EmptyState>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      {topics.length > 0 && <div className="flex min-h-14 shrink-0 flex-wrap items-center gap-3 border-b px-4 py-2 sm:px-6">
        <Sparkles className="size-[18px] text-primary" />
        <h1 className="text-[15px] font-semibold">Заметки</h1>
        {topics.length > 0 && (
          <span className="text-sm text-muted-foreground">
            · {topics.length} {plural(topics.length, ["тема", "темы", "тем"])}
          </span>
        )}
        <div className="relative ml-auto">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Искать во всех темах…"
            aria-label="Поиск по заметкам"
            className="h-9 w-40 pl-8 sm:w-52"
          />
        </div>
        <button
          type="button"
          onClick={() => void sync()}
          disabled={syncing}
          aria-label="Обновить заметки"
          title="Подтянуть изменения с сервера"
          className="flex size-8 items-center justify-center rounded text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
        >
          <RefreshCw className={syncing ? "size-4 animate-spin" : "size-4"} />
        </button>
        <Button size="sm" onClick={() => setComposerOpen(true)}>
          <Plus className="size-4" />
          Тема
        </Button>
      </div>}

      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-6">
        {composerOpen && (
          <NewTopicComposer
            onClose={() => setComposerOpen(false)}
            onCreated={(t) => {
              setTopics((prev) => [t, ...(prev ?? [])]);
              setComposerOpen(false);
            }}
          />
        )}

        {filtered.length === 0 ? (
          query.trim() ? (
            <EmptyState icon={SearchX} title="Совпадений пока нет" description="Попробуйте другие слова или сбросьте поиск, чтобы увидеть все темы.">
              <Button variant="outline" onClick={() => setQuery("")}>Сбросить поиск</Button>
            </EmptyState>
          ) : !composerOpen && (
            <EmptyState icon={FolderPlus} title="С какой темы начнём?" description="Собирайте связанные заметки в темы. Создайте первую здесь или попросите агента сохранить важное из разговора в заметку.">
              <Button onClick={() => setComposerOpen(true)}><Plus className="size-4" />Создать тему</Button>
              <Button variant="outline" disabled={syncing} onClick={() => void sync()}>
                <RefreshCw className={syncing ? "size-4 animate-spin" : "size-4"} />Обновить заметки
              </Button>
            </EmptyState>
          )
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {filtered.map((t) => (
              <TopicCard key={t.id} topic={t} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// TopicCard — карточка темы на полке: цветная полоса, аватар, имя+мета, обзор-клэмп (serif),
// пара последних заметок. Вся карточка — ссылка в тему.
function TopicCard({ topic }: { topic: Topic }) {
  return (
    <Link
      to={`/memory/${topic.id}`}
      className="group relative flex flex-col overflow-hidden rounded-xl border bg-card transition-colors hover:border-foreground/25"
    >
      <div className="h-[3px] w-full" style={{ background: topic.color }} />
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center gap-2.5">
          <Avatar topic={topic} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-[15px] font-semibold leading-tight">
              {topic.name}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {noteCountLabel(topic.noteCount)}
              {topic.updated && ` · ${topic.updated}`}
            </div>
          </div>
        </div>

        {topic.synthesis ? (
          <p className="line-clamp-3 font-serif text-[12.5px] leading-[1.55] text-muted-foreground">
            {topic.synthesis}
          </p>
        ) : (
          <p className="font-serif text-[12.5px] italic leading-[1.55] text-muted-foreground/70">
            Обзор пока не собран.
          </p>
        )}

        {topic.recent.length > 0 && (
          <div className="mt-auto flex flex-col gap-1.5 border-t pt-3">
            {topic.recent.map((n) => (
              <div key={n.id} className="flex items-center gap-2">
                <span
                  className="size-[5px] shrink-0 rounded-full"
                  style={{ background: noteType(n.type).color }}
                />
                <span className="truncate text-[13px] text-foreground/80">
                  {n.title || n.body}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Link>
  );
}

// Avatar — квадрат-аватар темы: буква/цифра на мягкой заливке цвета темы.
function Avatar({ topic, size = 38 }: { topic: Topic; size?: number }) {
  return (
    <span
      className="flex shrink-0 items-center justify-center rounded-[10px] font-semibold"
      style={{
        width: size,
        height: size,
        background: softColor(topic.color),
        color: topic.color,
        fontSize: size * 0.42,
      }}
    >
      {topic.initial}
    </span>
  );
}

// NewTopicComposer — инлайн-композер темы над сеткой: имя + выбор цвета.
function NewTopicComposer({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (t: Topic) => void;
}) {
  const [name, setName] = useState("");
  const [color, setColor] = useState(TOPIC_COLORS[0]);
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();

  async function submit() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const res = await memoryClient.createTopic({ name: name.trim(), color });
      if (!res.topic) throw new Error("пустой ответ CreateTopic");
      onCreated(res.topic);
      navigate(`/memory/${res.topic.id}`); // сразу открываем новую тему
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось создать тему",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mb-5 rounded-xl border bg-card p-4">
      <div className="flex flex-col gap-3">
        <Input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void submit()}
          placeholder="Название темы"
          className="font-medium"
        />
        <div className="flex items-center gap-2">
          {TOPIC_COLORS.map((c) => (
            <button
              key={c}
              type="button"
              onClick={() => setColor(c)}
              className={cn(
                "size-[26px] rounded-full ring-offset-2 ring-offset-card transition",
                color === c && "ring-2 ring-foreground",
              )}
              style={{ background: c }}
              aria-label={`Цвет ${c}`}
            />
          ))}
          <div className="ml-auto flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
              Отмена
            </Button>
            <Button size="sm" onClick={() => void submit()} disabled={busy || !name.trim()}>
              {busy && <Loader2 className="size-4 animate-spin" />}
              Создать тему
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
