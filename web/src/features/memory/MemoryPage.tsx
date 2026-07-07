import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Code, ConnectError } from "@connectrpc/connect";
import { Loader2, Plus, Search, Sparkles } from "lucide-react";
import { toast } from "sonner";

import { memoryClient } from "@/api/client";
import type { Topic } from "@/api/gen/brigade/v1/memory_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

  useEffect(() => {
    let alive = true;
    memoryClient
      .listTopics({ query: "" })
      .then((r) => {
        if (alive) setTopics(r.topics);
      })
      .catch((err) => {
        if (!alive) return;
        setTopics([]);
        if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
          setConfigured(false);
          return;
        }
        toast.error(
          err instanceof ConnectError ? err.rawMessage : "Не удалось загрузить темы",
        );
      });
    return () => {
      alive = false;
    };
  }, []);

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

  if (!configured) {
    return (
      <div className="mx-auto h-full w-full max-w-5xl px-6 py-8">
        <Header count={0} />
        <p className="mt-6 text-sm text-muted-foreground">
          Память ещё не настроена. Укажите свой приватный git-репозиторий заметок и
          SSH-ключ в{" "}
          <Link to="/settings" className="underline hover:text-foreground">
            Настройках → Память
          </Link>
          .
        </p>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 shrink-0 items-center gap-3 border-b px-6">
        <Sparkles className="size-[18px] text-primary" />
        <h1 className="text-[15px] font-semibold">Память</h1>
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
            className="h-9 w-52 pl-8"
          />
        </div>
        <Button size="sm" onClick={() => setComposerOpen(true)}>
          <Plus className="size-4" />
          Тема
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-6">
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
          <p className="text-sm text-muted-foreground">
            {query.trim()
              ? "Ничего не найдено."
              : "Пока нет тем. Создай тему кнопкой «Тема» — или агент сложит сюда заметки из сессии."}
          </p>
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

function Header({ count }: { count: number }) {
  return (
    <div className="flex items-center gap-2">
      <Sparkles className="size-[18px] text-primary" />
      <h1 className="text-[15px] font-semibold">Память</h1>
      {count > 0 && (
        <span className="text-sm text-muted-foreground">· {count}</span>
      )}
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
