import { useState, type ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Check, Loader2, BrainCircuit } from "lucide-react";
import { useMessage } from "@assistant-ui/react";

import { memoryClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { NOTE_TYPES, softColor } from "../memory/notes";

// SaveNoteCard — нативная карточка «Добавить в память» в ленте ACP-чата (tool-call save_note).
// Черновик приходит от навыка /note; поля редактируемые. По «Сохранить» карточка САМА шлёт
// запрос через brigade API (memoryClient.createNote) — без участия агента, затем показывает
// «сохранено». Пока карточка в ПОСЛЕДНЕМ сообщении — активна; как только пришло следующее
// сообщение (isLast=false) — черновик устарел (outdated) и сворачивается в одну строку. Тема
// задаётся именем — бэкенд резолвит/создаёт.
export function SaveNoteCard({
  args,
  sessionId,
}: {
  args: {
    title?: string;
    body?: string;
    topic?: string;
    sub?: string;
    type?: string;
    tags?: unknown;
  };
  sessionId?: string;
}) {
  // isLast — карточка в последнем сообщении треда. После следующего сообщения станет false →
  // черновик устарел (сохранять уже нельзя, чтобы не плодить заметки из старых карточек).
  const isLast = useMessage((m) => m.isLast);

  const [title, setTitle] = useState(args.title ?? "");
  const [body, setBody] = useState(args.body ?? "");
  const [topic, setTopic] = useState(args.topic ?? "");
  const [sub, setSub] = useState(args.sub ?? "");
  const [type, setType] = useState(
    NOTE_TYPES.some((t) => t.value === args.type) ? (args.type as string) : "reference",
  );
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const tags = Array.isArray(args.tags)
    ? args.tags.filter((t): t is string => typeof t === "string")
    : [];

  // Сохранённый черновик остаётся «готовым», даже когда сообщение перестало быть последним.
  const outdated = !isLast && !saved;

  const save = async () => {
    if (saving || saved || outdated || !title.trim()) return;
    setSaving(true);
    setError(null);
    try {
      const res = await memoryClient.createNote({
        title: title.trim(),
        body: body.trim(),
        type,
        topic: topic.trim(),
        sub: sub.trim(),
        from: "из чата",
        session: sessionId ?? "",
        tags,
        layer: "semantic",
      });
      if (!res.note) throw new Error("пустой ответ");
      setSaved(true);
    } catch (err) {
      setError(err instanceof ConnectError ? err.rawMessage : "Не удалось сохранить");
      setSaving(false);
    }
  };

  if (saved) {
    return (
      <div className="flex items-center gap-2.5 rounded-lg border bg-card p-3.5 text-sm">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-success/15">
          <Check className="size-3.5 text-success" />
        </span>
        <span>
          Сохранено в память · <span className="font-medium">{topic.trim() || "Общее"}</span>
          {sub.trim() && <span className="text-muted-foreground"> / {sub.trim()}</span>}
        </span>
      </div>
    );
  }

  // Устаревший черновик сворачиваем в одну строку — не занимает место полной формой.
  if (outdated) {
    return (
      <div className="flex items-center gap-2.5 rounded-lg border bg-card/60 p-3 text-sm text-muted-foreground">
        <BrainCircuit className="size-4 shrink-0 opacity-70" />
        <span className="min-w-0 truncate">
          Черновик устарел
          {title.trim() && <span className="text-foreground/70"> · {title.trim()}</span>}
          {" — вызови "}
          <code className="text-foreground/70">/note</code>
          {" заново"}
        </span>
      </div>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border bg-card p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <BrainCircuit className="size-4 text-primary" />
        Добавить в память
      </div>

      <Field label="Заголовок">
        <Input
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={saving}
          placeholder="Суть в одну строку"
        />
      </Field>

      <Field label="Текст">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          disabled={saving}
          rows={4}
          placeholder="Текст заметки (markdown)"
          className="w-full resize-y rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-60"
        />
      </Field>

      <div className="grid grid-cols-2 gap-3">
        <Field label="Тема">
          <Input value={topic} onChange={(e) => setTopic(e.target.value)} disabled={saving} placeholder="Общее" />
        </Field>
        <Field label="Подтема">
          <Input value={sub} onChange={(e) => setSub(e.target.value)} disabled={saving} placeholder="Общее" />
        </Field>
      </div>

      <Field label="Тип">
        <div className="flex flex-wrap gap-1.5">
          {NOTE_TYPES.map((nt) => {
            const active = type === nt.value;
            return (
              <button
                key={nt.value}
                type="button"
                disabled={saving}
                onClick={() => setType(nt.value)}
                className={cn(
                  "flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs disabled:cursor-default",
                  saving ? "" : "cursor-pointer",
                  active ? "text-foreground" : "border-border text-muted-foreground",
                )}
                style={
                  active ? { borderColor: nt.color, background: softColor(nt.color) } : undefined
                }
              >
                <span className="size-2 rounded-full" style={{ background: nt.color }} />
                {nt.label}
              </button>
            );
          })}
        </div>
      </Field>

      {error && <div className="text-xs text-destructive">{error}</div>}

      <div className="flex justify-end">
        {/* Фиксированная ширина: смена «Сохранить» ↔ спиннер+«Сохранение…» не меняет размер
            кнопки (иначе она дёргается при быстром сохранении). */}
        <Button
          size="sm"
          onClick={() => void save()}
          disabled={saving || !title.trim()}
          className="min-w-[9.5rem] justify-center"
        >
          {saving ? (
            <>
              <Loader2 className="size-4 animate-spin" />
              Сохранение…
            </>
          ) : (
            "Сохранить"
          )}
        </Button>
      </div>
    </div>
  );
}

// Field — подпись + контрол карточки.
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </div>
      {children}
    </div>
  );
}
