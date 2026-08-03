import { useEffect, useMemo, useState } from "react";
import { Check, Copy, ExternalLinkIcon, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  agentClient,
  authClient,
  mcpClient,
  telegramClient,
} from "@/api/client";
import type { AgentType } from "@/api/gen/brigade/v1/agent_pb";
import type { AgentImagesSettings } from "@/api/gen/brigade/v1/auth_pb";
import type { McpServer } from "@/api/gen/brigade/v1/mcp_pb";
import type { TelegramBot } from "@/api/gen/brigade/v1/telegram_pb";
import { Button } from "@/components/ui/button";
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
  DangerZone,
  Description,
  ExternalLink,
  FieldLabel,
  Loading,
  SecretNote,
  SectionHeader,
  Toggle,
  errorText,
} from "./ui";

const BASE_IMAGE = "__base__";

type Draft = {
  id: string;
  agentType: string;
  authProfile: string;
  image: string;
  mcpServerIds: string[];
};

const emptyDraft = (): Draft => ({
  id: "",
  agentType: "claude-code",
  authProfile: "claude-token",
  image: "",
  mcpServerIds: [],
});

export function TelegramSection({
  bots,
  mode,
  selectedId,
  onSelect,
  onChange,
}: {
  bots: TelegramBot[] | null;
  mode: string;
  selectedId: string;
  onSelect: (id: string) => void;
  onChange: (bots: TelegramBot[]) => void;
}) {
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [token, setToken] = useState("");
  const [agents, setAgents] = useState<AgentType[] | null>(null);
  const [mcp, setMcp] = useState<McpServer[]>([]);
  const [images, setImages] = useState<AgentImagesSettings | null>(null);
  const [codexProfiles, setCodexProfiles] = useState<string[]>([]);
  const [claudeReady, setClaudeReady] = useState(false);
  const [bindingURL, setBindingURL] = useState("");
  const [saving, setSaving] = useState(false);
  const [binding, setBinding] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let alive = true;
    void Promise.all([
      agentClient.listAgentTypes({}),
      mcpClient.listServers({}),
      authClient.getAgentImages({}),
      authClient.getCodexSettings({}),
      authClient.getClaudeSettings({}),
    ]).then(([agentResult, mcpResult, imageResult, codex, claude]) => {
      if (!alive) return;
      setAgents(agentResult.agentTypes.filter((agent) => agent.supportedKinds.includes("acp")));
      setMcp(mcpResult.servers);
      setImages(imageResult);
      setCodexProfiles([
        ...(codex.chatgptConnected ? ["chatgpt"] : []),
        ...(codex.apiKeySet ? ["api-key"] : []),
      ]);
      setClaudeReady(claude.tokenSet);
    }).catch(() => alive && setAgents([]));
    return () => { alive = false; };
  }, []);

  const selected = useMemo(
    () => bots?.find((bot) => bot.id === draft.id),
    [bots, draft.id],
  );

  useEffect(() => {
    if (!bots) return;
    const bot = selectedId === "new"
      ? undefined
      : bots.find((item) => item.id === selectedId) ?? bots[0];
    if (!bot) {
      setDraft(emptyDraft());
      setToken("");
      setBindingURL("");
      return;
    }
    if (!selectedId) onSelect(bot.id);
    setDraft({
      id: bot.id,
      agentType: bot.agentType,
      authProfile: bot.authProfile,
      image: bot.image,
      mcpServerIds: bot.mcpServerIds,
    });
    setToken("");
    setBindingURL("");
  }, [bots, selectedId, onSelect]);

  useEffect(() => {
    if (draft.agentType === "codex") {
      if (!codexProfiles.includes(draft.authProfile)) {
        setDraft((current) => ({ ...current, authProfile: codexProfiles[0] ?? "" }));
      }
    } else if (draft.authProfile !== "claude-token") {
      setDraft((current) => ({ ...current, authProfile: "claude-token" }));
    }
  }, [draft.agentType, draft.authProfile, codexProfiles]);

  useEffect(() => {
    if (!bindingURL || selected?.ownerConnected) return;
    const timer = window.setInterval(() => {
      void telegramClient.listBots({})
        .then((result) => onChange(result.bots))
        .catch(() => window.clearInterval(timer));
    }, 2000);
    return () => window.clearInterval(timer);
  }, [bindingURL, selected?.ownerConnected, onChange]);

  if (!bots || agents === null) return <Loading />;

  const patch = (next: Partial<Draft>) => setDraft((current) => ({ ...current, ...next }));
  const agentReady = draft.agentType === "codex" ? Boolean(draft.authProfile) : claudeReady;

  const save = async () => {
    setSaving(true);
    try {
      const saved = await telegramClient.saveBot({
        bot: {
          id: draft.id,
          agentType: draft.agentType,
          authProfile: draft.authProfile,
          image: draft.image,
          mcpServerIds: draft.mcpServerIds,
        },
        token,
      });
      onChange([...bots.filter((bot) => bot.id !== saved.id), saved]);
      onSelect(saved.id);
      setToken("");
      toast.success("Telegram-бот сохранён");
      if (!saved.ownerConnected) {
        const link = await telegramClient.createBindingLink({ id: saved.id });
        setBindingURL(link.url);
      }
    } catch (error) {
      toast.error(errorText(error, "Не удалось сохранить Telegram-бота"));
    } finally {
      setSaving(false);
    }
  };

  const createBinding = async () => {
    if (!draft.id) return;
    setBinding(true);
    try {
      const result = await telegramClient.createBindingLink({ id: draft.id });
      setBindingURL(result.url);
    } catch (error) {
      toast.error(errorText(error, "Не удалось создать ссылку привязки"));
    } finally {
      setBinding(false);
    }
  };

  const remove = async () => {
    if (!draft.id) return;
    try {
      await telegramClient.deleteBot({ id: draft.id });
      const next = bots.filter((bot) => bot.id !== draft.id);
      onChange(next);
      onSelect(next[0]?.id ?? "new");
      toast.success("Telegram-бот удалён");
    } catch (error) {
      toast.error(errorText(error, "Не удалось удалить Telegram-бота"));
    }
  };

  const copyBinding = async () => {
    await navigator.clipboard.writeText(bindingURL);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  };

  return (
    <>
      <SectionHeader
        title={selected ? `@${selected.username}` : "Новый Telegram-бот"}
        badge={selected && (
          <Badge on={selected.ownerConnected}>
            {selected.ownerConnected ? "подключён" : "не привязан"}
          </Badge>
        )}
      >
        <Description>
          Ваш бот становится персональным интерфейсом Brigade. В группах и Guest Mode он
          отвечает только привязанному владельцу. Updates получает инстанс через {" "}
          <span className="font-mono">{mode}</span>.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        <FieldLabel>{selected?.tokenSet ? "Новый BotFather token" : "BotFather token"}</FieldLabel>
        <Input
          type="password"
          value={token}
          onChange={(event) => setToken(event.target.value)}
          placeholder={selected?.tokenSet ? "Пусто — не менять" : "123456:ABC…"}
          autoComplete="off"
          className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px]"
        />
        <SecretNote>Шифруется на сервере и обратно не отдаётся</SecretNote>
      </div>

      <div className="grid grid-cols-2 gap-3.5">
        <div className="flex flex-col gap-2">
          <FieldLabel>Агент</FieldLabel>
          <Select value={draft.agentType} onValueChange={(agentType) => patch({ agentType })}>
            <SelectTrigger className="h-[41px] w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              {agents.map((agent) => <SelectItem key={agent.id} value={agent.id}>{agent.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        {draft.agentType === "codex" && (
          <div className="flex flex-col gap-2">
            <FieldLabel>Авторизация</FieldLabel>
            <Select value={draft.authProfile} onValueChange={(authProfile) => patch({ authProfile })}>
              <SelectTrigger className="h-[41px] w-full"><SelectValue placeholder="Не настроена" /></SelectTrigger>
              <SelectContent>
                {codexProfiles.includes("chatgpt") && <SelectItem value="chatgpt">ChatGPT Plus</SelectItem>}
                {codexProfiles.includes("api-key") && <SelectItem value="api-key">OpenAI API key</SelectItem>}
              </SelectContent>
            </Select>
          </div>
        )}
      </div>

      {images && images.images.length > 0 && (
        <div className="flex flex-col gap-2">
          <FieldLabel>Docker-образ</FieldLabel>
          <Select value={draft.image || BASE_IMAGE} onValueChange={(image) => patch({ image: image === BASE_IMAGE ? "" : image })}>
            <SelectTrigger className="h-[41px] w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={BASE_IMAGE}>Базовый ({images.defaultImage})</SelectItem>
              {images.images.map((image) => <SelectItem key={image.image} value={image.image}>{image.image}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      )}

      {mcp.length > 0 && (
        <div className="flex flex-col gap-2">
          <FieldLabel>MCP-серверы</FieldLabel>
          <div className="divide-y overflow-hidden rounded-[11px] border bg-[#1c1b1a]">
            {mcp.map((server) => {
              const on = draft.mcpServerIds.includes(server.id);
              return (
                <button
                  key={server.id}
                  type="button"
                  aria-pressed={on}
                  onClick={() => patch({
                    mcpServerIds: on
                      ? draft.mcpServerIds.filter((id) => id !== server.id)
                      : [...draft.mcpServerIds, server.id],
                  })}
                  className="flex w-full items-center gap-3 px-3.5 py-3 text-left transition-colors hover:bg-[#232221]"
                >
                  <span className="min-w-0 flex-1 truncate text-[13px]">{server.name}</span>
                  <Toggle on={on} />
                </button>
              );
            })}
          </div>
        </div>
      )}

      {!agentReady && (
        <p className="text-[12px] text-destructive">
          Сначала настройте авторизацию выбранного агента.
        </p>
      )}

      <div className="flex items-center gap-2">
        <Button disabled={saving || !agentReady || (!draft.id && !token.trim())} onClick={() => void save()}>
          {saving && <Loader2 className="size-4 animate-spin" />}
          Сохранить
        </Button>
        {selected && (
          <Button variant="outline" disabled={binding} onClick={() => void createBinding()}>
            {binding && <Loader2 className="size-4 animate-spin" />}
            {selected.ownerConnected ? "Перепривязать владельца" : "Подключить Telegram"}
          </Button>
        )}
      </div>

      {bindingURL && (
        <div className="flex flex-col gap-2 rounded-[11px] border bg-[#1c1b1a] p-3.5">
          <div className="text-[13px]">Откройте ссылку под своим Telegram-аккаунтом</div>
          <div className="flex gap-2">
            <Button asChild size="sm">
              <a href={bindingURL} target="_blank" rel="noreferrer">
                <ExternalLinkIcon className="size-3.5" /> Открыть Telegram
              </a>
            </Button>
            <Button variant="outline" size="sm" onClick={() => void copyBinding()}>
              {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
              {copied ? "Скопировано" : "Копировать"}
            </Button>
          </div>
          <p className="text-[11.5px] text-[#6c695f]">Ссылка одноразовая и действует 15 минут.</p>
        </div>
      )}

      {selected && (
        <div className="flex flex-col gap-2 rounded-[11px] border bg-[#1c1b1a] p-3.5 text-[12px]">
          <div className="flex justify-between gap-3">
            <span className="text-muted-foreground">Владелец</span>
            <span>{selected.ownerConnected ? `@${selected.ownerUsername || "подключён"}` : "не привязан"}</span>
          </div>
          <div className="flex justify-between gap-3">
            <span className="text-muted-foreground">Топики в личном чате</span>
            <span>{selected.hasTopicsEnabled ? "включены" : "включите Threaded Mode"}</span>
          </div>
          <div className="flex justify-between gap-3">
            <span className="text-muted-foreground">Guest Mode</span>
            <span>{selected.supportsGuestQueries ? "доступен" : "не включён"}</span>
          </div>
          {(!selected.hasTopicsEnabled || !selected.supportsGuestQueries) && (
            <p className="border-t pt-2 text-[#6c695f]">
              Возможности включаются в <ExternalLink href="https://t.me/BotFather">@BotFather</ExternalLink>, затем сохраните бота ещё раз для обновления статуса.
            </p>
          )}
        </div>
      )}

      {selected && (
        <DangerZone title="Удалить подключение" hint="Сессии Brigade останутся, бот перестанет получать сообщения">
          <Button variant="ghost" className="text-destructive" onClick={() => void remove()}>
            <Trash2 className="size-4" /> Удалить
          </Button>
        </DangerZone>
      )}
    </>
  );
}
