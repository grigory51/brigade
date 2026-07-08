import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import {
  ChevronLeft,
  Loader2,
  MessageSquare,
  MoreHorizontal,
  Pencil,
  Plus,
  Sparkles,
  Trash2,
  Undo2,
} from "lucide-react";
import { toast } from "sonner";

import { memoryClient } from "@/api/client";
import type { Note, Topic } from "@/api/gen/brigade/v1/memory_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Markdown } from "@/components/markdown";
import { cn } from "@/lib/utils";
import { NOTE_TYPES, noteType, softColor, TOPIC_COLORS } from "./notes";

// ungrouped — под какой подтемой показывать заметки с пустым sub (дефолтная подтема темы).
const UNSORTED = "Общее";

// TopicPage — экран одной темы: обзор «своими словами» + заметки, сгруппированные по подтемам.
export function TopicPage() {
  const { topicId = "" } = useParams();
  const navigate = useNavigate();
  const [topic, setTopic] = useState<Topic | null | undefined>(undefined);
  const [notes, setNotes] = useState<Note[]>([]);
  // activeSub — фильтр подтем: "all" | название подтемы.
  const [activeSub, setActiveSub] = useState("all");
  const [composerOpen, setComposerOpen] = useState(false);
  const [editingTopic, setEditingTopic] = useState(false);

  useEffect(() => {
    let alive = true;
    setTopic(undefined);
    memoryClient
      .getTopic({ id: topicId })
      .then((r) => {
        if (!alive) return;
        setTopic(r.topic ?? null);
        setNotes(r.notes);
      })
      .catch((err) => {
        if (!alive) return;
        setTopic(null);
        toast.error(
          err instanceof ConnectError ? err.rawMessage : "Не удалось загрузить тему",
        );
      });
    return () => {
      alive = false;
    };
  }, [topicId]);

  // Подтемы + счётчики. Пустой sub заметки относим к дефолтной подтеме UNSORTED.
  const subs = useMemo(() => {
    const counts = new Map<string, number>();
    for (const n of notes) {
      const s = n.sub || UNSORTED;
      counts.set(s, (counts.get(s) ?? 0) + 1);
    }
    const ordered = [...(topic?.subs ?? [])];
    for (const s of counts.keys()) if (!ordered.includes(s)) ordered.push(s);
    return ordered
      .filter((s) => (counts.get(s) ?? 0) > 0)
      .map((s) => ({ name: s, count: counts.get(s) ?? 0 }));
  }, [notes, topic]);

  const groups = useMemo(() => {
    const visible =
      activeSub === "all" ? subs : subs.filter((s) => s.name === activeSub);
    return visible.map((s) => ({
      sub: s.name,
      notes: notes.filter((n) => (n.sub || UNSORTED) === s.name),
    }));
  }, [subs, notes, activeSub]);

  if (topic === undefined) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Loader2 className="size-5 animate-spin" />
      </div>
    );
  }
  if (topic === null) {
    return (
      <div className="flex h-full flex-col">
        <TopicHeader />
        <p className="p-6 text-sm text-muted-foreground">Тема не найдена.</p>
      </div>
    );
  }

  function patchNote(updated: Note) {
    setNotes((prev) => prev.map((n) => (n.id === updated.id ? updated : n)));
  }
  function removeNote(id: string) {
    setNotes((prev) => prev.filter((n) => n.id !== id));
  }

  return (
    <div className="flex h-full flex-col">
      <TopicHeader
        topic={topic}
        onAdd={() => setComposerOpen(true)}
        onEdit={() => setEditingTopic(true)}
        onDeleted={() => navigate("/memory")}
      />

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-7 py-6">
          {editingTopic && (
            <TopicEditor
              topic={topic}
              onSaved={(t) => {
                setTopic(t);
                setEditingTopic(false);
              }}
              onClose={() => setEditingTopic(false)}
            />
          )}
          <OverviewBlock topic={topic} onSaved={(t) => setTopic(t)} />

          {subs.length > 0 && (
            <div className="mb-5 flex flex-wrap gap-1.5">
              <SubChip
                label={`Все ${notes.length}`}
                active={activeSub === "all"}
                onClick={() => setActiveSub("all")}
              />
              {subs.map((s) => (
                <SubChip
                  key={s.name}
                  label={`${s.name} ${s.count}`}
                  active={activeSub === s.name}
                  onClick={() => setActiveSub(s.name)}
                />
              ))}
            </div>
          )}

          {composerOpen && (
            <NoteComposer
              topic={topic}
              subs={subs.map((s) => s.name)}
              onClose={() => setComposerOpen(false)}
              onCreated={(n) => {
                setNotes((prev) => [n, ...prev]);
                setActiveSub("all");
                setComposerOpen(false);
              }}
            />
          )}

          {notes.length === 0 ? (
            <EmptyTopic onAdd={() => setComposerOpen(true)} />
          ) : (
            <div className="flex flex-col gap-6">
              {groups.map((g) => (
                <div key={g.sub}>
                  <div className="mb-2 flex items-center gap-2">
                    <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted-foreground">
                      {g.sub}
                    </span>
                    <span className="rounded-full bg-muted px-1.5 text-[11px] text-muted-foreground">
                      {g.notes.length}
                    </span>
                    <div className="h-px flex-1 bg-border" />
                  </div>
                  <div className="flex flex-col">
                    {g.notes.map((n) => (
                      <NoteRow
                        key={n.id}
                        note={n}
                        topic={topic}
                        subs={subs.map((s) => s.name)}
                        onMoved={patchNote}
                        onDeleted={removeNote}
                      />
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// TopicHeader — шапка с крестами: «‹ Все темы» / аватар / имя / N заметок; справа «+ Заметка»
// и меню темы (переименовать / удалить). Меню скрыто для виртуальной «Общее».
function TopicHeader({
  topic,
  onAdd,
  onEdit,
  onDeleted,
}: {
  topic?: Topic;
  onAdd?: () => void;
  onEdit?: () => void;
  onDeleted?: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  async function del() {
    setMenuOpen(false);
    if (!topic) return;
    if (!window.confirm(`Удалить тему «${topic.name}» со всеми заметками?`)) return;
    setDeleting(true);
    try {
      await memoryClient.deleteTopic({ id: topic.id });
      onDeleted?.();
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось удалить тему",
      );
      setDeleting(false);
    }
  }

  const canManage = topic && topic.id !== "general";

  return (
    <div className="flex h-14 shrink-0 items-center gap-3 border-b px-6">
      <Link
        to="/memory"
        className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ChevronLeft className="size-4" />
        Все темы
      </Link>
      {topic && (
        <>
          <span
            className="flex size-7 shrink-0 items-center justify-center rounded-lg text-xs font-semibold"
            style={{ background: softColor(topic.color), color: topic.color }}
          >
            {topic.initial}
          </span>
          <span className="truncate text-[15px] font-semibold">{topic.name}</span>
          <span className="shrink-0 text-sm text-muted-foreground">
            · {topic.noteCount}
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <Button size="sm" onClick={onAdd}>
              <Plus className="size-4" />
              Заметка
            </Button>
            {canManage && (
              <div className="relative">
                <button
                  type="button"
                  onClick={() => setMenuOpen((v) => !v)}
                  disabled={deleting}
                  className="flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted disabled:opacity-50"
                >
                  {deleting ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <MoreHorizontal className="size-4" />
                  )}
                </button>
                {menuOpen && (
                  <>
                    <button
                      type="button"
                      className="fixed inset-0 z-10 cursor-default"
                      onClick={() => setMenuOpen(false)}
                      aria-label="Закрыть меню"
                    />
                    <div className="absolute right-0 top-9 z-20 w-48 overflow-hidden rounded-[10px] border border-border/80 bg-popover py-1 shadow-lg">
                      <button
                        type="button"
                        onClick={() => {
                          setMenuOpen(false);
                          onEdit?.();
                        }}
                        className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-accent"
                      >
                        <Pencil className="size-3.5 text-muted-foreground" />
                        Переименовать
                      </button>
                      <div className="my-1 h-px bg-border" />
                      <button
                        type="button"
                        onClick={() => void del()}
                        className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-[#e08a6f] hover:bg-[rgba(201,100,66,0.14)]"
                      >
                        <Trash2 className="size-3.5" />
                        Удалить тему
                      </button>
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// TopicEditor — инлайн-редактор темы: переименование + смена цвета.
function TopicEditor({
  topic,
  onSaved,
  onClose,
}: {
  topic: Topic;
  onSaved: (t: Topic) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(topic.name);
  const [color, setColor] = useState(topic.color);
  const [busy, setBusy] = useState(false);

  async function save() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const res = await memoryClient.updateTopic({
        id: topic.id,
        name: name.trim(),
        color,
      });
      if (res.topic) onSaved(res.topic);
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось сохранить тему",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mb-5 rounded-xl border bg-card p-4">
      <div className="mb-3 text-[11px] font-bold uppercase tracking-wide text-muted-foreground">
        Тема
      </div>
      <div className="flex flex-col gap-3">
        <Input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void save()}
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
            <Button size="sm" onClick={() => void save()} disabled={busy || !name.trim()}>
              {busy && <Loader2 className="size-4 animate-spin" />}
              Сохранить
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

// OverviewBlock — блок «О чём эта тема»: serif-обзор, редактируемый вручную.
function OverviewBlock({
  topic,
  onSaved,
}: {
  topic: Topic;
  onSaved: (t: Topic) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(topic.synthesis);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      const res = await memoryClient.updateTopicOverview({
        id: topic.id,
        synthesis: text.trim(),
      });
      if (res.topic) onSaved(res.topic);
      setEditing(false);
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось сохранить обзор",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="mb-6 rounded-xl border bg-card p-5 pl-6"
      style={{ borderLeft: `3px solid ${topic.color}` }}
    >
      <div className="mb-2.5 flex items-center gap-2">
        <Sparkles className="size-[15px]" style={{ color: topic.color }} />
        <span
          className="text-[11px] font-bold uppercase tracking-[0.08em]"
          style={{ color: topic.color }}
        >
          О чём эта тема
        </span>
        <span className="text-[11px] text-muted-foreground/70">собрано агентом</span>
        {!editing && (
          <button
            type="button"
            onClick={() => {
              setText(topic.synthesis);
              setEditing(true);
            }}
            className="ml-auto text-muted-foreground/70 transition-colors hover:text-foreground"
            title="Изменить обзор"
          >
            <Pencil className="size-3.5" />
          </button>
        )}
      </div>

      {editing ? (
        <div className="flex flex-col gap-3">
          <Textarea
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={5}
            placeholder="Свяжи мысли темы в связный обзор своими словами…"
            style={{ borderColor: topic.color }}
          />
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setEditing(false)} disabled={busy}>
              Отмена
            </Button>
            <Button size="sm" onClick={() => void save()} disabled={busy}>
              {busy && <Loader2 className="size-4 animate-spin" />}
              Сохранить
            </Button>
          </div>
        </div>
      ) : topic.synthesis ? (
        <div className="font-serif text-[15.5px] leading-[1.7] text-foreground/90">
          <Markdown>{topic.synthesis}</Markdown>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setEditing(true)}
          className="w-full text-left font-serif text-[15px] italic leading-[1.7] text-muted-foreground/55 transition-colors hover:text-muted-foreground"
        >
          Обзор пока пуст. Напиши, о чём эта тема, своими словами.
        </button>
      )}
    </div>
  );
}

// SubChip — пилюля-фильтр подтемы.
function SubChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-full border px-3 py-1 text-[12.5px] transition-colors",
        active
          ? "border-primary bg-primary/15 text-primary"
          : "border-border text-muted-foreground hover:bg-accent",
      )}
    >
      {label}
    </button>
  );
}

// NoteRow — строка заметки: точка типа + заголовок/тип + тело + провенанс; меню ⋯.
function NoteRow({
  note,
  topic,
  subs,
  onMoved,
  onDeleted,
}: {
  note: Note;
  topic: Topic;
  subs: string[];
  onMoved: (n: Note) => void;
  onDeleted: (id: string) => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  // busy — идёт move/delete (синхронный git-push занимает секунды): гасим строку и крутим
  // спиннер, чтобы действие не выглядело «зависшим».
  const [busy, setBusy] = useState(false);
  const t = noteType(note.type);

  async function move(toSub: string) {
    setMenuOpen(false);
    setBusy(true);
    try {
      const res = await memoryClient.moveNote({
        id: note.id,
        toTopicId: topic.id,
        toSub,
      });
      if (res.note) onMoved(res.note);
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось переместить",
      );
      setBusy(false);
    }
  }

  async function remove() {
    setMenuOpen(false);
    setBusy(true);
    try {
      await memoryClient.deleteNote({ id: note.id });
      onDeleted(note.id);
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось удалить",
      );
      setBusy(false);
    }
  }

  return (
    <div
      className={cn(
        "group relative -mx-3 flex gap-2.5 rounded-lg px-3 py-2.5 hover:bg-accent/60",
        busy && "pointer-events-none opacity-50",
      )}
    >
      <span
        className="mt-[7px] size-[9px] shrink-0 rounded-full"
        style={{ background: t.color }}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="text-[14.5px] font-semibold">{note.title || "—"}</span>
          <span className="text-[11px]" style={{ color: t.color }}>
            {t.label}
          </span>
        </div>
        {note.body && (
          <p className="mt-0.5 whitespace-pre-wrap text-[13px] leading-[1.55] text-muted-foreground">
            {note.body}
          </p>
        )}
        <div className="mt-1 flex items-center gap-1.5 text-[11.5px] text-muted-foreground/70">
          {note.from && (
            <>
              <MessageSquare className="size-3" />
              <span className="truncate">{note.from}</span>
            </>
          )}
          <span className="ml-auto">{note.updated || note.created}</span>
        </div>
      </div>

      <button
        type="button"
        onClick={() => setMenuOpen((v) => !v)}
        disabled={busy}
        className={cn(
          "absolute right-2 top-2 flex size-[26px] items-center justify-center rounded-md text-muted-foreground transition hover:bg-muted",
          busy ? "opacity-100" : "opacity-0 group-hover:opacity-100",
        )}
      >
        {busy ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <MoreHorizontal className="size-4" />
        )}
      </button>

      {menuOpen && (
        <>
          <button
            type="button"
            className="fixed inset-0 z-10 cursor-default"
            onClick={() => setMenuOpen(false)}
            aria-label="Закрыть меню"
          />
          <div className="absolute right-2 top-9 z-20 w-48 overflow-hidden rounded-[10px] border border-border/80 bg-popover py-1 shadow-lg">
            {subs.filter((s) => s !== (note.sub || UNSORTED)).length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-bold uppercase tracking-wide text-muted-foreground">
                  Переместить в
                </div>
                {subs
                  .filter((s) => s !== (note.sub || UNSORTED))
                  .map((s) => (
                    <button
                      key={s}
                      type="button"
                      onClick={() => void move(s)}
                      className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-accent"
                    >
                      <Undo2 className="size-3.5 text-muted-foreground" />
                      {s}
                    </button>
                  ))}
                <div className="my-1 h-px bg-border" />
              </>
            )}
            <button
              type="button"
              onClick={() => void remove()}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-[#e08a6f] hover:bg-[rgba(201,100,66,0.14)]"
            >
              <Trash2 className="size-3.5" />
              Удалить
            </button>
          </div>
        </>
      )}
    </div>
  );
}

// NoteComposer — инлайн-композер заметки: суть + детали + тип + подтема.
function NoteComposer({
  topic,
  subs,
  onClose,
  onCreated,
}: {
  topic: Topic;
  subs: string[];
  onClose: () => void;
  onCreated: (n: Note) => void;
}) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [type, setType] = useState<string>("idea");
  const [sub, setSub] = useState(subs[0] ?? UNSORTED);
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!title.trim()) return;
    setBusy(true);
    try {
      const res = await memoryClient.createNote({
        title: title.trim(),
        body: body.trim(),
        type,
        topicId: topic.id,
        sub: sub === UNSORTED ? "" : sub,
        from: "добавлено вручную",
        tags: [],
        session: "",
        layer: "semantic",
      });
      if (!res.note) throw new Error("пустой ответ CreateNote");
      onCreated(res.note);
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось сохранить заметку",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mb-5 rounded-xl border border-border/80 bg-[#2f2e2c] p-4">
      <div className="mb-3 text-[13px] font-medium text-muted-foreground">
        Новая заметка вручную
      </div>
      <div className="flex flex-col gap-3">
        <Input
          autoFocus
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Суть в одну строку"
          className="font-semibold"
        />
        <Textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Детали, контекст, цифры…"
          rows={3}
        />

        <div>
          <div className="mb-1.5 text-[11px] font-bold uppercase tracking-wide text-muted-foreground">
            Тип
          </div>
          <div className="flex flex-wrap gap-1.5">
            {NOTE_TYPES.map((nt) => {
              const active = type === nt.value;
              return (
                <button
                  key={nt.value}
                  type="button"
                  onClick={() => setType(nt.value)}
                  className={cn(
                    "flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[12.5px] transition",
                    active ? "text-foreground" : "border-border text-muted-foreground",
                  )}
                  style={
                    active
                      ? { borderColor: nt.color, background: softColor(nt.color) }
                      : undefined
                  }
                >
                  <span className="size-2 rounded-full" style={{ background: nt.color }} />
                  {nt.label}
                </button>
              );
            })}
          </div>
        </div>

        {subs.length > 0 && (
          <div>
            <div className="mb-1.5 text-[11px] font-bold uppercase tracking-wide text-muted-foreground">
              Подтема
            </div>
            <div className="flex flex-wrap gap-1.5">
              {subs.map((s) => (
                <SubChip key={s} label={s} active={sub === s} onClick={() => setSub(s)} />
              ))}
            </div>
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
            Отмена
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy || !title.trim()}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            Сохранить в тему
          </Button>
        </div>
      </div>
    </div>
  );
}

// EmptyTopic — пустое состояние темы.
function EmptyTopic({ onAdd }: { onAdd: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12 text-center">
      <div className="flex size-11 items-center justify-center rounded-lg bg-muted">
        <Plus className="size-5 text-muted-foreground" />
      </div>
      <div className="text-sm text-muted-foreground">
        Пока пусто. Добавь первую заметку в эту тему.
      </div>
      <Button size="sm" variant="outline" onClick={onAdd}>
        Добавить заметку
      </Button>
    </div>
  );
}
