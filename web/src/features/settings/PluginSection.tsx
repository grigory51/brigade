import { useCallback, useEffect, useRef, useState } from "react";
import { Boxes, ChevronDown, FileUp, Link as LinkIcon, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { pluginClient, refreshSession } from "@/api/client";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Badge, Description, FieldLabel, Loading, SecretNote, SectionHeader, errorText } from "./ui";

type ConfigField = {
  type: "string" | "number" | "boolean" | "file" | "directory";
  title?: string;
  description?: string;
  required?: boolean;
  multiple?: boolean;
  sensitive?: boolean;
  default?: unknown;
  min?: number;
  max?: number;
};

const decode = <T,>(value: Uint8Array, fallback: T): T => {
  if (!value.length) return fallback;
  try { return JSON.parse(new TextDecoder().decode(value)) as T; } catch { return fallback; }
};

export function PluginSection() {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [target, setTarget] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState("");
  const [editing, setEditing] = useState("");
  const [sourceUrl, setSourceUrl] = useState("");
  const [values, setValues] = useState<Record<string, unknown>>({});
  const fileRef = useRef<HTMLInputElement>(null);

  const reload = useCallback(async () => {
    const result = await pluginClient.list({});
    setPlugins(result.plugins);
    setTarget(result.requiredTarget);
    return result.plugins;
  }, []);

  useEffect(() => { void reload().catch(() => setPlugins([])); }, [reload]);

  const edit = (item: Plugin) => {
    const initial = decode<Record<string, unknown>>(item.configValuesJson, {});
    const schema = decode<Record<string, ConfigField>>(item.configSchemaJson, {});
    for (const [key, field] of Object.entries(schema)) {
      if (!(key in initial) && field.default !== undefined) initial[key] = field.default;
    }
    setValues(initial);
    const source = item.variants.find((variant) => variant.version === item.version && (variant.target === target || variant.target === "portable"))?.source
      ?? item.variants.find((variant) => variant.source.startsWith("https://"))?.source
      ?? "";
    setSourceUrl(source.startsWith("https://") ? source : "");
    setEditing(item.id);
  };

  const installURL = async () => {
    if (!url.trim()) return;
    setBusy("install");
    try {
      const item = await pluginClient.install({ url: url.trim() });
      setUrl("");
      await reload();
      edit(item);
      toast.success(`${item.name} установлен`);
    } catch (error) {
      toast.error(errorText(error, "Не удалось установить MCP App"));
    } finally { setBusy(""); }
  };

  const upload = async (file: File) => {
    setBusy("upload");
    try {
      const send = () => fetch("/api/plugins/upload", {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/octet-stream", "X-Filename": encodeURIComponent(file.name) },
          body: file,
        });
      let response = await send();
      if (response.status === 401) { await refreshSession(); response = await send(); }
      if (!response.ok) throw new Error(await response.text());
      const { id } = await response.json() as { id: string };
      const installed = (await reload()).find((item) => item.id === id);
      if (installed) edit(installed);
      toast.success(`${file.name} установлен`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Не удалось загрузить MCP App");
    } finally { setBusy(""); if (fileRef.current) fileRef.current.value = ""; }
  };

  const saveConfig = async (item: Plugin) => {
    setBusy(item.id);
    try {
      await pluginClient.saveConfig({ id: item.id, valuesJson: new TextEncoder().encode(JSON.stringify(values)) });
      setEditing("");
      await reload();
      toast.success("Настройки сохранены");
    } catch (error) { toast.error(errorText(error, "Не удалось сохранить настройки")); }
    finally { setBusy(""); }
  };

  const update = async (item: Plugin) => {
    setBusy(item.id);
    try {
      const updated = await pluginClient.update({ id: item.id, url: sourceUrl.trim() });
      await reload();
      edit(updated);
      toast.success(`${item.name} обновлён`);
    }
    catch (error) { toast.error(errorText(error, "Не удалось обновить MCP App")); }
    finally { setBusy(""); }
  };

  const remove = async (item: Plugin) => {
    setBusy(item.id);
    try { await pluginClient.delete({ id: item.id }); await reload(); toast.success(`${item.name} удалён`); }
    catch (error) { toast.error(errorText(error, "Не удалось удалить MCP App")); }
    finally { setBusy(""); }
  };

  if (plugins === null) return <Loading />;

  return (
    <>
      <SectionHeader title="MCP Apps" badge={<Badge on={plugins.length > 0}>{plugins.length ? `${plugins.length} шт.` : "нет"}</Badge>}>
        <Description>
          Полноценные интерфейсы с MCP-сервером. Для текущей среды нужна сборка <span className="font-mono text-foreground">{target}</span>.
          {" "}Устанавливайте только доверенные пакеты: они выполняют код в среде агента.
        </Description>
      </SectionHeader>

      <div className="space-y-2 rounded-xl border bg-card/30 p-3">
        <FieldLabel>Добавить по HTTPS-ссылке на .mcpb</FieldLabel>
        <div className="flex gap-2">
          <Input value={url} disabled={busy === "install"} onChange={(event) => setUrl(event.target.value)} placeholder="https://…/application.mcpb" onKeyDown={(event) => { if (event.key === "Enter") void installURL(); }} />
          <Button onClick={() => void installURL()} disabled={!url.trim() || busy !== ""}>
            {busy === "install" ? <Loader2 className="size-4 animate-spin" /> : <LinkIcon className="size-4" />} Установить
          </Button>
        </div>
        <div className="flex items-center gap-2 pt-1">
          <input ref={fileRef} type="file" accept=".mcpb,application/zip" className="hidden" onChange={(event) => { const file = event.target.files?.[0]; if (file) void upload(file); }} />
          <Button variant="outline" disabled={busy !== ""} onClick={() => fileRef.current?.click()}>
            {busy === "upload" ? <Loader2 className="size-4 animate-spin" /> : <FileUp className="size-4" />} Загрузить .mcpb
          </Button>
          <span className="text-[11.5px] text-muted-foreground">Manifest и платформа проверятся до установки.</span>
        </div>
      </div>

      <div className="space-y-2">
        {plugins.map((item) => {
          const schema = decode<Record<string, ConfigField>>(item.configSchemaJson, {});
          const updating = busy === item.id;
          return (
            <div key={item.id} className="rounded-xl border bg-card/40 p-3">
              <div className="flex items-start gap-3">
                <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary"><Boxes className="size-4 text-primary" /></div>
                <button type="button" className="min-w-0 flex-1 text-left" onClick={() => editing === item.id ? setEditing("") : edit(item)}>
                  <div className="flex items-center gap-2"><span className="truncate text-sm font-medium">{item.name}</span><Badge on={item.compatible}>{item.compatible ? "совместим" : "недоступен для среды"}</Badge><ChevronDown className={`ml-auto size-4 shrink-0 text-muted-foreground transition-transform ${editing === item.id ? "rotate-180" : ""}`} /></div>
                  <p className="line-clamp-2 text-xs text-muted-foreground">{item.description}</p>
                  <div className="mt-1 flex flex-wrap gap-1.5 font-mono text-[10.5px] text-muted-foreground">{item.variants.map((variant) => <span key={`${variant.version}-${variant.target}`}>{variant.version} · {variant.target}</span>)}</div>
                </button>
                {!item.system && <>
                  <Button size="icon" variant="ghost" aria-label="Удалить" disabled={updating} onClick={() => void remove(item)}><Trash2 className="size-4" /></Button>
                </>}
              </div>

              {editing === item.id && (
                <div className="mt-3 space-y-3 border-t pt-3">
                  <div className="space-y-1.5">
                    <FieldLabel>URL пакета</FieldLabel>
                    <div className="flex gap-2">
                      <Input value={sourceUrl} disabled={updating} placeholder="https://…/application.mcpb" onChange={(event) => setSourceUrl(event.target.value)} />
                      <Button disabled={updating || !sourceUrl.trim()} onClick={() => void update(item)}>
                        {updating && <Loader2 className="size-4 animate-spin" />}Сохранить
                      </Button>
                    </div>
                    <p className="text-[11.5px] text-muted-foreground">Bundle будет скачан и проверен заново.{item.system ? " Сохранится как ваша версия приложения." : ""}</p>
                  </div>
                  {Object.entries(schema).map(([key, field]) => (
                    <label key={key} className="block space-y-1.5">
                      <FieldLabel>{field.title || key}{field.required ? " *" : ""}</FieldLabel>
                      {field.type === "boolean" ? (
                        <Checkbox checked={Boolean(values[key])} onCheckedChange={(checked) => setValues((current) => ({ ...current, [key]: checked === true }))} />
                      ) : (
                        <Input
                          type={field.sensitive ? "password" : field.type === "number" ? "number" : "text"}
                          min={field.min} max={field.max}
                          value={field.sensitive ? "" : Array.isArray(values[key]) ? values[key].join(", ") : String(values[key] ?? "")}
                          placeholder={field.sensitive && item.configuredSecrets.includes(key) ? "Секрет уже сохранён" : undefined}
                          onChange={(event) => setValues((current) => ({ ...current, [key]: field.type === "number" ? Number(event.target.value) : field.multiple ? event.target.value.split(",").map((part) => part.trim()).filter(Boolean) : event.target.value }))}
                        />
                      )}
                      {field.description && <p className="text-[11.5px] text-muted-foreground">{field.description}</p>}
                      {field.sensitive && <SecretNote>Значение хранится в vault и обратно не возвращается.</SecretNote>}
                    </label>
                  ))}
                  {Object.keys(schema).length > 0 && <Button disabled={updating} onClick={() => void saveConfig(item)}>{updating && <Loader2 className="size-4 animate-spin" />}Сохранить настройки</Button>}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </>
  );
}
