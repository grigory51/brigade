import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Code, ConnectError } from "@connectrpc/connect";
import { Loader2, NotebookPen, Plus, Search } from "lucide-react";
import { toast } from "sonner";
import { memoryClient } from "@/api/client";
import type { Note } from "@/api/gen/brigade/v1/memory_pb";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

// noteTypes — типы заметок (в соответствии с бэкендом memory.noteTypes).
const noteTypes = [
  "idea",
  "decision",
  "insight",
  "todo",
  "question",
  "reference",
] as const;

// MemoryPage — личная память: список заметок с поиском и созданием. Поиск — клиентский
// фильтр по одному разу загруженному списку (при личных объёмах серверный запрос не нужен).
export function MemoryPage() {
  const [notes, setNotes] = useState<Note[] | null>(null);
  const [query, setQuery] = useState("");
  const [dialogOpen, setDialogOpen] = useState(false);
  // layer — активный слой памяти: semantic (факты) | episodic (саммари сессий).
  const [layer, setLayer] = useState<"semantic" | "episodic">("semantic");
  // configured=false — у пользователя не задан git-репозиторий памяти (RPC вернул
  // failed_precondition); показываем подсказку про Настройки вместо ошибки.
  const [configured, setConfigured] = useState(true);

  useEffect(() => {
    let alive = true;
    memoryClient
      .listNotes({ query: "" })
      .then((r) => {
        if (alive) setNotes(r.notes);
      })
      .catch((err) => {
        if (!alive) return;
        setNotes([]);
        if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
          setConfigured(false);
          return;
        }
        toast.error(
          err instanceof ConnectError
            ? err.rawMessage
            : "Не удалось загрузить заметки",
        );
      });
    return () => {
      alive = false;
    };
  }, []);

  const filtered = useMemo(() => {
    if (!notes) return [];
    // Слой: заметки без поля layer (старые) считаем семантическими.
    const byLayer = notes.filter((n) => (n.layer || "semantic") === layer);
    const q = query.trim().toLowerCase();
    if (!q) return byLayer;
    return byLayer.filter(
      (n) =>
        n.title.toLowerCase().includes(q) ||
        n.body.toLowerCase().includes(q) ||
        n.tags.some((t) => t.toLowerCase().includes(q)),
    );
  }, [notes, query, layer]);

  if (notes === null) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Loader2 className="size-5 animate-spin" />
      </div>
    );
  }

  if (!configured) {
    return (
      <div className="mx-auto h-full w-full max-w-4xl overflow-y-auto px-6 py-8">
        <div className="mb-6 flex items-center gap-2">
          <NotebookPen className="size-5 text-muted-foreground" />
          <h1 className="text-lg font-semibold">Заметки</h1>
        </div>
        <p className="text-sm text-muted-foreground">
          Память ещё не настроена. Укажите свой приватный git-репозиторий заметок и SSH-ключ
          в{" "}
          <Link to="/settings" className="underline hover:text-foreground">
            Настройках → Память
          </Link>
          .
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto h-full w-full max-w-4xl overflow-y-auto px-6 py-8">
      <div className="mb-6 flex items-center gap-2">
        <NotebookPen className="size-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">Заметки</h1>
        <Button
          size="sm"
          className="ml-auto"
          onClick={() => setDialogOpen(true)}
        >
          <Plus className="size-4" />
          Новая
        </Button>
      </div>

      <div className="mb-4 flex gap-1">
        {(["semantic", "episodic"] as const).map((l) => (
          <Button
            key={l}
            size="sm"
            variant={layer === l ? "default" : "outline"}
            onClick={() => setLayer(l)}
          >
            {l === "semantic" ? "Факты" : "Сессии"}
          </Button>
        ))}
      </div>

      <div className="relative mb-4">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Поиск по заголовку, тексту, тегам"
          className="pl-9"
        />
      </div>

      {filtered.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {query.trim()
            ? "Ничего не найдено."
            : layer === "episodic"
              ? "Саммари сессий появятся здесь. Попроси агента «сохрани сессию в память»."
              : "Пока пусто. Агент складывает сюда факты через /brigade:memory, или создай кнопкой «Новая»."}
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {filtered.map((n) => (
            <div
              key={n.id}
              className="flex flex-col gap-2 rounded-lg border bg-card p-4"
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="min-w-0 truncate font-medium">
                  {n.title || n.id}
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {n.updated}
                </span>
              </div>
              <p className="line-clamp-4 whitespace-pre-wrap text-sm text-muted-foreground">
                {n.body}
              </p>
              <div className="mt-auto flex flex-wrap items-center gap-1.5 pt-1">
                <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                  {n.type}
                </span>
                {n.tags.map((t) => (
                  <span
                    key={t}
                    className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground"
                  >
                    #{t}
                  </span>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      <NewNoteDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreated={(note) => setNotes((prev) => [note, ...(prev ?? [])])}
      />
    </div>
  );
}

// NewNoteDialog — создание заметки из UI (title/body/type/tags → MemoryService.CreateNote).
function NewNoteDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (note: Note) => void;
}) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [type, setType] = useState<string>("idea");
  const [layer, setLayer] = useState<"semantic" | "episodic">("semantic");
  const [tags, setTags] = useState("");
  const [busy, setBusy] = useState(false);

  async function onSubmit() {
    if (!body.trim()) return;
    setBusy(true);
    try {
      const res = await memoryClient.createNote({
        title: title.trim(),
        body: body.trim(),
        type,
        layer,
        tags: tags
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
        session: "",
      });
      if (!res.note) throw new Error("пустой ответ CreateNote");
      onCreated(res.note);
      onOpenChange(false);
      setTitle("");
      setBody("");
      setTags("");
      setType("idea");
      setLayer("semantic");
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось сохранить заметку",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Новая заметка</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label>Заголовок</Label>
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Коротко о чём заметка"
            />
          </div>
          <div className="space-y-2">
            <Label>Текст</Label>
            <Textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="Markdown"
              rows={6}
            />
          </div>
          <div className="flex gap-3">
            <div className="space-y-2">
              <Label>Слой</Label>
              <Select
                value={layer}
                onValueChange={(v) => setLayer(v as "semantic" | "episodic")}
              >
                <SelectTrigger className="w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="semantic">Факт</SelectItem>
                  <SelectItem value="episodic">Сессия</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex-1 space-y-2">
              <Label>Тип</Label>
              <Select value={type} onValueChange={setType}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {noteTypes.map((t) => (
                    <SelectItem key={t} value={t}>
                      {t}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-2">
            <Label>Теги</Label>
            <Input
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="через запятую"
            />
          </div>
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            Отмена
          </Button>
          <Button onClick={() => void onSubmit()} disabled={busy || !body.trim()}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            Сохранить
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
