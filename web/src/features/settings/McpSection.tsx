import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2, Lock, Pencil, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { mcpClient } from "@/api/client";
import {
  McpTransport,
  type McpSecret,
  type McpServer,
} from "@/api/gen/brigade/v1/mcp_pb";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Badge,
  Code,
  Description,
  FieldLabel,
  Loading,
  SecretNote,
  SectionHeader,
  errorText,
} from "./ui";

/**
 * Разделы настроек «MCP-серверы» и «Секреты».
 *
 * MCP-сервер даёт агенту дополнительные инструменты. Значения переменных окружения и
 * заголовков — либо литерал, либо ссылка на секрет из соседнего раздела: секрет остаётся
 * на сервере, а в конфиге виден только его именованный слот.
 */

// SECRET_REF — та же форма ссылки, что понимает бэкенд (internal/mcp): ссылок может быть
// сколько угодно и в любом месте значения.
const SECRET_REF = /\$\{secret\.([A-Za-z0-9_]+)\}/g;

// secretRefs — имена секретов, на которые ссылается значение.
function secretRefs(value: string): string[] {
  return [...value.matchAll(SECRET_REF)].map((m) => m[1]);
}

const TRANSPORT_LABEL: Record<number, string> = {
  [McpTransport.STDIO]: "stdio",
  [McpTransport.HTTP]: "http",
  [McpTransport.SSE]: "sse",
};

type Pair = { name: string; value: string };

// Draft — конфиг в форме, удобной для полей ввода: аргументы одной строкой, пары env и
// headers в общем списке (по транспорту он значит одно или другое).
type Draft = {
  id: string;
  name: string;
  transport: McpTransport;
  command: string;
  argsText: string;
  url: string;
  pairs: Pair[];
};

const emptyDraft: Draft = {
  id: "",
  name: "",
  transport: McpTransport.STDIO,
  command: "",
  argsText: "",
  url: "",
  pairs: [],
};

function toDraft(srv: McpServer): Draft {
  return {
    id: srv.id,
    name: srv.name,
    transport: srv.transport,
    command: srv.command,
    argsText: srv.args.join(" "),
    url: srv.url,
    pairs: (srv.transport === McpTransport.STDIO ? srv.env : srv.headers).map(
      (kv) => ({ name: kv.name, value: kv.value }),
    ),
  };
}

export function McpSection({ onCountChange }: { onCountChange: (n: number) => void }) {
  const [servers, setServers] = useState<McpServer[] | null>(null);
  // Секреты живут здесь же: они нужны только MCP-серверам, а списку в форме — сразу
  // после добавления, без перезагрузки раздела.
  const [secrets, setSecrets] = useState<McpSecret[]>([]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);

  const reload = useCallback(async () => {
    const res = await mcpClient.listServers({});
    setServers(res.servers);
    onCountChange(res.servers.length);
  }, [onCountChange]);

  const reloadSecrets = useCallback(async () => {
    const res = await mcpClient.listSecrets({});
    setSecrets(res.secrets);
  }, []);

  useEffect(() => {
    void reload().catch(() => setServers([]));
    void reloadSecrets().catch(() => setSecrets([]));
  }, [reload, reloadSecrets]);

  const secretNames = secrets.map((s) => s.name);

  const save = useCallback(async () => {
    if (!draft) return;
    const stdio = draft.transport === McpTransport.STDIO;
    const pairs = draft.pairs.filter((p) => p.name.trim());
    const server = {
      id: draft.id,
      name: draft.name.trim(),
      transport: draft.transport,
      command: stdio ? draft.command.trim() : "",
      // Аргументы разбираются по пробелам: кавычки и экранирование не поддерживаются —
      // если аргумент содержит пробел, его место в переменной окружения.
      args: stdio ? draft.argsText.trim().split(/\s+/).filter(Boolean) : [],
      url: stdio ? "" : draft.url.trim(),
      env: stdio ? pairs : [],
      headers: stdio ? [] : pairs,
    };
    setSaving(true);
    try {
      if (draft.id) {
        await mcpClient.updateServer({ server });
      } else {
        await mcpClient.createServer({ server });
      }
      setDraft(null);
      await reload();
      toast.success("Сервер сохранён");
    } catch (err) {
      toast.error(errorText(err, "Не удалось сохранить сервер"));
    } finally {
      setSaving(false);
    }
  }, [draft, reload]);

  const remove = useCallback(
    async (srv: McpServer) => {
      try {
        await mcpClient.deleteServer({ id: srv.id });
        await reload();
        toast.success(`Сервер ${srv.name} удалён`);
      } catch (err) {
        toast.error(errorText(err, "Не удалось удалить сервер"));
      }
    },
    [reload],
  );

  if (servers === null) return <Loading />;

  return (
    <>
      <SectionHeader
        title="MCP-серверы"
        badge={
          <Badge on={servers.length > 0}>
            {servers.length > 0 ? `${servers.length} шт.` : "нет серверов"}
          </Badge>
        }
      >
        <Description>
          MCP-сервер даёт агенту дополнительные инструменты (задачи, база, документы).
          Набор включается для каждой сессии отдельно — при её создании, а в открытой
          ACP-сессии его меняет чип <Code>MCP</Code>. CLI-сессия читает набор при запуске
          агента: смена набора подхватится со следующим запуском.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        {servers.map((srv) => (
          <div
            key={srv.id}
            className="flex items-center gap-3 rounded-[10px] border bg-card px-3 py-2.5"
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate text-[13px]">{srv.name}</span>
                <span className="shrink-0 rounded-[5px] bg-secondary px-1.5 text-[10.5px] text-muted-foreground">
                  {TRANSPORT_LABEL[srv.transport] ?? "?"}
                </span>
              </div>
              <div className="truncate font-mono text-[11.5px] text-[#7a776f]">
                {srv.transport === McpTransport.STDIO
                  ? [srv.command, ...srv.args].join(" ")
                  : srv.url}
              </div>
            </div>
            <button
              type="button"
              aria-label="Изменить"
              onClick={() => setDraft(toDraft(srv))}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            >
              <Pencil className="size-3.5" />
            </button>
            <button
              type="button"
              aria-label="Удалить"
              onClick={() => void remove(srv)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-destructive"
            >
              <Trash2 className="size-3.5" />
            </button>
          </div>
        ))}

        {!draft && (
          <Button
            variant="outline"
            className="self-start"
            onClick={() => setDraft(emptyDraft)}
          >
            <Plus className="size-4" />
            Добавить сервер
          </Button>
        )}
      </div>

      {draft && (
        <ServerForm
          draft={draft}
          secretNames={secretNames}
          saving={saving}
          onChange={setDraft}
          onCancel={() => setDraft(null)}
          onSave={() => void save()}
        />
      )}

      <SecretsBlock secrets={secrets} onChanged={() => void reloadSecrets()} />
    </>
  );
}

// ServerForm — форма создания и правки сервера. Поля зависят от транспорта: stdio просит
// команду с аргументами и переменные окружения, http/sse — URL и заголовки.
function ServerForm({
  draft,
  secretNames,
  saving,
  onChange,
  onCancel,
  onSave,
}: {
  draft: Draft;
  secretNames: string[];
  saving: boolean;
  onChange: (d: Draft) => void;
  onCancel: () => void;
  onSave: () => void;
}) {
  const stdio = draft.transport === McpTransport.STDIO;
  const patch = (next: Partial<Draft>) => onChange({ ...draft, ...next });
  const patchPair = (i: number, next: Partial<Pair>) =>
    patch({
      pairs: draft.pairs.map((p, j) => (j === i ? { ...p, ...next } : p)),
    });

  // Ссылки на секреты, которых нет: их легко получить, удалив секрет после того, как на
  // него сослались. Сервер отвергнет такой конфиг при создании сессии — предупреждаем
  // раньше, в форме.
  const unknownRefs = [
    ...new Set(
      draft.pairs.flatMap((p) =>
        secretRefs(p.value).filter((name) => !secretNames.includes(name)),
      ),
    ),
  ];

  return (
    <div className="flex flex-col gap-[18px] rounded-[12px] border bg-card p-4">
      <div className="flex gap-3">
        <div className="flex flex-1 flex-col gap-2">
          <FieldLabel>Имя</FieldLabel>
          <Input
            value={draft.name}
            placeholder="notion"
            onChange={(e) => patch({ name: e.target.value })}
            className="h-[38px] bg-[#1c1b1a] font-mono text-[12.5px]"
          />
        </div>
        <div className="flex w-[150px] flex-col gap-2">
          <FieldLabel>Транспорт</FieldLabel>
          <Select
            value={String(draft.transport)}
            onValueChange={(v) => patch({ transport: Number(v) as McpTransport })}
          >
            <SelectTrigger className="h-[38px] w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={String(McpTransport.STDIO)}>stdio</SelectItem>
              <SelectItem value={String(McpTransport.HTTP)}>http</SelectItem>
              <SelectItem value={String(McpTransport.SSE)}>sse</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      {stdio ? (
        <div className="flex gap-3">
          <div className="flex w-[150px] flex-col gap-2">
            <FieldLabel>Команда</FieldLabel>
            <Input
              value={draft.command}
              placeholder="npx"
              onChange={(e) => patch({ command: e.target.value })}
              className="h-[38px] bg-[#1c1b1a] font-mono text-[12.5px]"
            />
          </div>
          <div className="flex flex-1 flex-col gap-2">
            <FieldLabel>Аргументы</FieldLabel>
            <Input
              value={draft.argsText}
              placeholder="-y @modelcontextprotocol/server-everything"
              onChange={(e) => patch({ argsText: e.target.value })}
              className="h-[38px] bg-[#1c1b1a] font-mono text-[12.5px]"
            />
          </div>
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <FieldLabel>URL</FieldLabel>
          <Input
            value={draft.url}
            placeholder="https://mcp.notion.com/mcp"
            onChange={(e) => patch({ url: e.target.value })}
            className="h-[38px] bg-[#1c1b1a] font-mono text-[12.5px]"
          />
        </div>
      )}

      <div className="flex flex-col gap-2">
        <FieldLabel>
          {stdio ? "Переменные окружения" : "Заголовки запросов"}
        </FieldLabel>
        {draft.pairs.map((pair, i) => {
          return (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={pair.name}
                placeholder={stdio ? "API_TOKEN" : "Authorization"}
                onChange={(e) => patchPair(i, { name: e.target.value })}
                className="h-[36px] w-[190px] bg-[#1c1b1a] font-mono text-[12px]"
              />
              <PairValue
                value={pair.value}
                secretNames={secretNames}
                onChange={(value) => patchPair(i, { value })}
              />
              <button
                type="button"
                aria-label="Убрать"
                onClick={() =>
                  patch({ pairs: draft.pairs.filter((_, j) => j !== i) })
                }
                className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-destructive"
              >
                <X className="size-3.5" />
              </button>
            </div>
          );
        })}
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          onClick={() =>
            patch({ pairs: [...draft.pairs, { name: "", value: "" }] })
          }
        >
          <Plus className="size-3.5" />
          {stdio ? "Переменная" : "Заголовок"}
        </Button>
        {unknownRefs.length > 0 && (
          <p className="text-[11.5px] text-destructive">
            Нет таких секретов: {unknownRefs.join(", ")} — сессия с этим сервером не
            создастся, пока ссылка не указывает на существующий секрет
          </p>
        )}
        <SecretNote>
          Значение может ссылаться на секреты (список ниже): <Code>{"${secret.ИМЯ}"}</Code>{" "}
          в любом месте строки и сколько угодно раз (<Code>{"Bearer ${secret.TOKEN}"}</Code>
          ). В конфиге остаются ссылки, значения сервер подставит при запуске сессии
        </SecretNote>
      </div>

      <div className="flex justify-end gap-2 border-t pt-3.5">
        <Button variant="outline" onClick={onCancel} disabled={saving}>
          Отмена
        </Button>
        <Button onClick={onSave} disabled={saving || !draft.name.trim()}>
          {saving && <Loader2 className="size-4 animate-spin" />}
          Сохранить
        </Button>
      </div>
    </div>
  );
}

// PairValue — поле значения переменной или заголовка. Обычный текст, в который кнопкой
// справа вставляется ссылка на секрет — в позицию курсора, чтобы «Bearer » набирался
// как есть, а секрет дописывался следом.
function PairValue({
  value,
  secretNames,
  onChange,
}: {
  value: string;
  secretNames: string[];
  onChange: (value: string) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  const insert = (name: string) => {
    const el = inputRef.current;
    const ref = `\${secret.${name}}`;
    const from = el?.selectionStart ?? value.length;
    const to = el?.selectionEnd ?? from;
    onChange(value.slice(0, from) + ref + value.slice(to));
    // Курсор после вставленной ссылки: значение приходит пропом, поэтому позицию
    // выставляем после перерисовки.
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(from + ref.length, from + ref.length);
    });
  };

  // Пока секретов нет, кнопки вставки нет вовсе: меню было бы пустым, а место под неё
  // в поле — занятым зря. Появится вместе с первым секретом.
  if (secretNames.length === 0) {
    return (
      <Input
        value={value}
        placeholder="значение"
        onChange={(e) => onChange(e.target.value)}
        className="h-[36px] flex-1 bg-[#1c1b1a] font-mono text-[12px]"
      />
    );
  }

  return (
    <div className="relative flex-1">
      <Input
        ref={inputRef}
        value={value}
        placeholder="значение или ${secret.ИМЯ}"
        onChange={(e) => onChange(e.target.value)}
        className="h-[36px] bg-[#1c1b1a] pr-9 font-mono text-[12px]"
      />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Вставить секрет"
            title="Вставить ссылку на секрет"
            className="absolute inset-y-0 right-0 flex w-8 items-center justify-center rounded-r-md text-muted-foreground transition-colors hover:bg-card hover:text-primary"
          >
            <Lock className="size-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {secretNames.map((name) => (
            <DropdownMenuItem
              key={name}
              onSelect={() => insert(name)}
              className="font-mono text-[12px]"
            >
              {name}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

// ─── Секреты (vault) ──────────────────────────────────────────────────────────

// SecretsBlock — хранилище токенов, блок в конце раздела MCP. Отдельным разделом настроек
// не выделен: секретами пользуются только MCP-серверы, и держать их рядом с местом, где на
// них ссылаются, короче для глаза.
function SecretsBlock({
  secrets,
  onChanged,
}: {
  secrets: McpSecret[];
  onChanged: () => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      await mcpClient.setSecret({ name: name.trim(), value });
      setName("");
      setValue("");
      onChanged();
      toast.success("Секрет сохранён");
    } catch (err) {
      toast.error(errorText(err, "Не удалось сохранить секрет"));
    } finally {
      setSaving(false);
    }
  }, [name, value, onChanged]);

  const remove = useCallback(
    async (secretName: string) => {
      try {
        await mcpClient.deleteSecret({ name: secretName });
        onChanged();
        toast.success(`Секрет ${secretName} удалён`);
      } catch (err) {
        toast.error(errorText(err, "Не удалось удалить секрет"));
      }
    },
    [onChanged],
  );

  return (
    <div className="flex flex-col gap-[18px] border-t pt-[18px]">
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2.5">
          <h3 className="text-[14px] font-semibold">Секреты</h3>
          <Badge on={secrets.length > 0}>
            {secrets.length > 0 ? `${secrets.length} шт.` : "пусто"}
          </Badge>
        </div>
        <Description>
          Токены и ключи, которыми авторизуются серверы выше. В конфиге сервера хранится
          только ссылка <Code>{"${secret.ИМЯ}"}</Code>; значение сервер подставляет в
          момент запуска сессии.
        </Description>
      </div>

      <div className="flex flex-col gap-2">
        {secrets.map((secret) => (
          <div
            key={secret.name}
            className="flex items-center gap-3 rounded-[10px] border bg-card px-3 py-2.5"
          >
            <Lock className="size-3.5 shrink-0 text-primary" />
            <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">
              {secret.name}
            </span>
            <span className="shrink-0 text-[11.5px] text-[#6c695f]">
              изменён {formatDate(secret.updatedAt)}
            </span>
            <button
              type="button"
              aria-label="Удалить"
              onClick={() => void remove(secret.name)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-destructive"
            >
              <Trash2 className="size-3.5" />
            </button>
          </div>
        ))}
        {secrets.length === 0 && (
          <p className="text-[12.5px] text-muted-foreground/70">
            Пока пусто. Добавьте первый секрет ниже.
          </p>
        )}
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel>Новый секрет</FieldLabel>
        <div className="flex items-start gap-2">
          <Input
            value={name}
            placeholder="NOTION_TOKEN"
            autoComplete="off"
            onChange={(e) => setName(e.target.value)}
            className="h-[41px] w-[220px] bg-[#1c1b1a] font-mono text-[12.5px]"
          />
          <Input
            type="password"
            value={value}
            placeholder="значение"
            autoComplete="off"
            onChange={(e) => setValue(e.target.value)}
            className="h-[41px] flex-1 bg-[#1c1b1a] font-mono text-[12.5px]"
          />
          <Button
            className="h-[41px]"
            disabled={saving || !name.trim() || !value}
            onClick={() => void save()}
          >
            {saving && <Loader2 className="size-4 animate-spin" />}
            Сохранить
          </Button>
        </div>
        <SecretNote>
          Шифруется на сервере и не отображается после сохранения. Секрет с тем же именем
          перезаписывается
        </SecretNote>
      </div>
    </div>
  );
}

// formatDate — дата последнего изменения секрета (Unix-секунды из protobuf приходят
// как bigint).
function formatDate(unixSec: bigint): string {
  return new Date(Number(unixSec) * 1000).toLocaleDateString("ru-RU", {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}
