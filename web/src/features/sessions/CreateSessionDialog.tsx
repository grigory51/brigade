import { useEffect, useState, type ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Boxes, Check, Loader2, MessageCircle, Terminal } from "lucide-react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import {
  agentClient,
  authClient,
  mcpClient,
  pluginClient,
  responseProfileClient,
  sessionClient,
} from "@/api/client";
import { AgentType, type AgentConnection } from "@/api/gen/brigade/v1/agent_pb";
import type { AgentImagesSettings } from "@/api/gen/brigade/v1/auth_pb";
import { McpServer } from "@/api/gen/brigade/v1/mcp_pb";
import { Session, SessionKind } from "@/api/gen/brigade/v1/session_pb";
import type { ResponseProfile } from "@/api/gen/brigade/v1/response_profile_pb";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

// Набор MCP-серверов и образ последнего запуска. Хранятся локально, а не на сервере: это
// удобство ввода, а не настройка пользователя.
const MCP_SELECTION_KEY = "brigade.mcp.lastSelection";
const IMAGE_KEY = "brigade.session.lastImage";

// BASE_IMAGE — значение пункта «Базовый образ» в селекте. Отдельный маркер нужен потому,
// что пустое значение у пункта Radix Select запрещено, а серверу базовый образ передаётся
// именно пустой строкой.
const BASE_IMAGE = "__base__";

function PluginCover({ plugin }: { plugin: Plugin }) {
  const [url, setUrl] = useState("");
  useEffect(() => {
    if (!plugin.cover.length) return;
    const next = URL.createObjectURL(new Blob([plugin.cover], { type: plugin.coverMimeType }));
    setUrl(next);
    return () => URL.revokeObjectURL(next);
  }, [plugin.cover, plugin.coverMimeType]);
  return url ? (
    <img src={url} alt="" className="size-full object-cover transition-transform duration-300 group-hover:scale-[1.025]" />
  ) : (
    <div className="flex size-full items-center justify-center bg-muted"><Boxes className="size-8 text-muted-foreground" /></div>
  );
}

function ExperienceTile({
  value,
  selected,
  disabled,
  title,
  description,
  preview,
  onSelect,
}: {
  value: string;
  selected: boolean;
  disabled?: boolean;
  title: string;
  description: string;
  preview: ReactNode;
  onSelect: (value: string) => void;
}) {
  return (
    <label className={cn(
      "group relative min-w-0 overflow-hidden rounded-xl border bg-card/40 text-left transition-[border-color,background-color,box-shadow] focus-within:ring-2 focus-within:ring-ring/60",
      selected ? "border-primary bg-primary/5 shadow-[0_8px_24px_rgba(0,0,0,.18)]" : "border-border hover:border-input hover:bg-card/70",
      disabled && "cursor-not-allowed opacity-45",
    )}>
      <input
        type="radio"
        name="experience"
        value={value}
        checked={selected}
        disabled={disabled}
        onChange={() => onSelect(value)}
        className="sr-only"
      />
      <div className="relative aspect-[16/8] overflow-hidden border-b border-border/70 bg-muted">
        {preview}
        {selected && (
          <span className="absolute top-2 right-2 flex size-5 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-sm">
            <Check className="size-3.5" strokeWidth={3} />
          </span>
        )}
      </div>
      <div className="space-y-1 p-3">
        <div className="truncate text-sm font-medium">{title}</div>
        <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">{description}</p>
      </div>
    </label>
  );
}

function loadMcpSelection(): string[] {
  try {
    const raw = localStorage.getItem(MCP_SELECTION_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? (parsed as string[]) : [];
  } catch {
    return [];
  }
}

function saveMcpSelection(ids: string[]) {
  try {
    localStorage.setItem(MCP_SELECTION_KEY, JSON.stringify(ids));
  } catch {
    // Приватный режим/переполнение — выбор просто не переживёт перезагрузку.
  }
}

export function CreateSessionDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (session: Session) => void;
}) {
  const [agents, setAgents] = useState<AgentType[] | null>(null);
  const [connections, setConnections] = useState<AgentConnection[] | null>(null);
  const [connectionId, setConnectionId] = useState("");
  const [kind, setKind] = useState<SessionKind>(SessionKind.CLI);
  const [busy, setBusy] = useState(false);
  // MCP-серверы пользователя и выбранный набор. Выбор помнится между запусками: набор
  // инструментов у человека обычно постоянный, отмечать его заново каждый раз незачем.
  const [mcpServers, setMcpServers] = useState<McpServer[]>([]);
  const [mcpSelected, setMcpSelected] = useState<string[]>(loadMcpSelection);
  // Образы контейнера агента: пустой выбор — базовый образ brigade.
  const [images, setImages] = useState<AgentImagesSettings | null>(null);
  const [image, setImage] = useState<string>(
    () => localStorage.getItem(IMAGE_KEY) ?? "",
  );
  const [responseProfiles, setResponseProfiles] = useState<ResponseProfile[]>([]);
  const [responseProfileId, setResponseProfileId] = useState("default");
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [experienceId, setExperienceId] = useState("");

  // Список типов агентов подгружается один раз при первом открытии диалога.
  // Режим взаимодействия (kind) выбирается независимо от агента, поэтому при
  // загрузке достаточно выбрать первого агента; kind остаётся на значении по
  // умолчанию (CLI).
  useEffect(() => {
    if (!open || agents !== null) return;
    let cancelled = false;
    agentClient
      .listAgentTypes({})
      .then((res) => {
        if (cancelled) return;
        setAgents(res.agentTypes);
      })
      .catch(() => {
        if (!cancelled) setAgents([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open, agents]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    agentClient.listConnections({})
      .then((result) => !cancelled && setConnections(result.connections))
      .catch(() => !cancelled && setConnections([]));
    return () => {
      cancelled = true;
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    pluginClient.list({})
      .then((result) => !cancelled && setPlugins(result.plugins))
      .catch(() => !cancelled && setPlugins([]));
    return () => { cancelled = true; };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setResponseProfileId("default");
    responseProfileClient.list({})
      .then((result) => !cancelled && setResponseProfiles(result.profiles))
      .catch(() => !cancelled && setResponseProfiles([]));
    return () => { cancelled = true; };
  }, [open]);

  // Список MCP-серверов перечитывается при открытии: он мог измениться в настройках.
  // Удалённые серверы вычищаются из запомненного выбора.
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    mcpClient
      .listServers({})
      .then((res) => {
        if (cancelled) return;
        setMcpServers(res.servers);
        const alive = new Set(res.servers.map((s) => s.id));
        setMcpSelected((prev) => prev.filter((id) => alive.has(id)));
      })
      .catch(() => {
        if (!cancelled) setMcpServers([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open]);

  // Образы перечитываются при открытии (могли измениться в настройках). Удалённый образ
  // не должен остаться выбранным — откатываемся на базовый.
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    authClient
      .getAgentImages({})
      .then((res) => {
        if (cancelled) return;
        setImages(res);
        setImage((prev) =>
          prev && !res.images.some((i) => i.image === prev) ? "" : prev,
        );
      })
      .catch(() => {
        if (!cancelled) setImages(null);
      });
    return () => {
      cancelled = true;
    };
  }, [open]);

  const selectedConnection = connections?.find((connection) => connection.id === connectionId);
  const selectedAgent = agents?.find((agent) => agent.id === selectedConnection?.agentType);
  const experience = experienceId ? `plugin:${experienceId}` : kind === SessionKind.CLI ? "cli" : "chat";
  const acpDisabled = selectedAgent?.supportedKinds.length ? !selectedAgent.supportedKinds.includes("acp") : false;

  const selectExperience = (value: string) => {
    if (value === "cli") {
      setKind(SessionKind.CLI);
      setExperienceId("");
    } else {
      setKind(SessionKind.ACP);
      setExperienceId(value.startsWith("plugin:") ? value.slice(7) : "");
    }
  };

  useEffect(() => {
    if (!connections || connections.some((connection) => connection.id === connectionId)) return;
    setConnectionId(connections[0]?.id ?? "");
  }, [connections, connectionId]);

  async function onSubmit() {
    if (!selectedConnection) return;
    setBusy(true);
    try {
      saveMcpSelection(mcpSelected);
      localStorage.setItem(IMAGE_KEY, image);
      const res = await sessionClient.create({
        agentType: selectedConnection.agentType,
        kind,
        prompt: "",
        mcpServerIds: mcpSelected,
        image,
        authProfile: selectedConnection.id,
        responseProfileId: kind === SessionKind.ACP ? responseProfileId : "default",
        experienceId,
      });
      const session = res.session;
      if (!session) throw new Error("пустой ответ Create");
      onOpenChange(false);
      onCreated(session);
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось создать сессию",
      );
    } finally {
      setBusy(false);
    }
  }

  const loading = agents === null || connections === null;
  const noAgents = !loading && connections.length === 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Новая сессия</DialogTitle>
          <DialogDescription>
            Выберите агента и параметры запуска.
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        ) : noAgents ? (
          <div className="flex flex-col items-center gap-3 py-6 text-center text-sm text-muted-foreground">
            <p>Нет подключённых агентов.</p>
            <Button asChild size="sm" onClick={() => onOpenChange(false)}>
              <Link to="/settings/agents">Настроить агента</Link>
            </Button>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Агент</Label>
              <Select value={connectionId} onValueChange={setConnectionId}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Выберите агента" />
                </SelectTrigger>
                <SelectContent>
                  {connections!.map((connection) => (
                    <SelectItem key={connection.id} value={connection.id}>
                      {connection.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label>Интерфейс</Label>
              <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
                <ExperienceTile
                  value="cli"
                  selected={experience === "cli"}
                  title="Console"
                  description="Полноценный терминал и CLI агента."
                  onSelect={selectExperience}
                  preview={(
                    <div className="flex size-full flex-col justify-center gap-2 bg-[#1f1e1d] px-5 font-mono text-[10px] text-muted-foreground">
                      <div className="flex items-center gap-2 text-[#e38a68]"><Terminal className="size-4" /><span>brigade</span></div>
                      <span className="h-1.5 w-4/5 rounded-full bg-[#4a4843]" />
                      <span className="h-1.5 w-3/5 rounded-full bg-[#3a3a37]" />
                    </div>
                  )}
                />
                <ExperienceTile
                  value="chat"
                  selected={experience === "chat"}
                  disabled={acpDisabled}
                  title="Chat"
                  description="Диалог, инструменты и интерактивные карточки."
                  onSelect={selectExperience}
                  preview={(
                    <div className="flex size-full flex-col justify-center gap-2 bg-[#353532] px-5">
                      <div className="flex items-center gap-2 text-xs text-muted-foreground"><MessageCircle className="size-4 text-[#e38a68]" /><span className="h-2 w-20 rounded-full bg-[#4a4843]" /></div>
                      <span className="ml-auto h-5 w-2/3 rounded-[8px_8px_2px_8px] bg-[#c96442]/75" />
                      <span className="h-2 w-4/5 rounded-full bg-[#4a4843]" />
                    </div>
                  )}
                />
                {plugins.map((plugin) => (
                  <ExperienceTile
                    key={plugin.id}
                    value={`plugin:${plugin.id}`}
                    selected={experience === `plugin:${plugin.id}`}
                    disabled={acpDisabled}
                    title={plugin.name}
                    description={plugin.description || "Предметный интерфейс с агентом."}
                    onSelect={selectExperience}
                    preview={<PluginCover plugin={plugin} />}
                  />
                ))}
              </div>
            </div>

            {kind === SessionKind.ACP && responseProfiles.length > 0 && (
              <div className="space-y-2">
                <Label>Профиль ответов</Label>
                <Select value={responseProfileId} onValueChange={setResponseProfileId}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {responseProfiles.map((profile) => (
                      <SelectItem key={profile.id} value={profile.id}>{profile.name}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}

            {images !== null && images.images.length > 0 && (
              <div className="space-y-2">
                <Label>Образ</Label>
                <Select
                  value={image || BASE_IMAGE}
                  onValueChange={(v) => setImage(v === BASE_IMAGE ? "" : v)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {/* Наружу базовый образ уходит пустой строкой (сервер подставит его
                        сам), но у пункта списка значение должно быть непустым. */}
                    <SelectItem value={BASE_IMAGE}>
                      Базовый ({images.defaultImage})
                    </SelectItem>
                    {images.images.map((img) => (
                      <SelectItem key={img.image} value={img.image}>
                        {img.image}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {kind === SessionKind.CLI && (
                  <p className="text-xs text-muted-foreground">
                    У CLI-сессий контейнер общий на пользователя: образ задаёт первая
                    сессия, смена применится, когда все они закрыты.
                  </p>
                )}
              </div>
            )}

            {mcpServers.length > 0 && (
              <div className="space-y-2">
                <Label>MCP-серверы</Label>
                <div className="flex flex-col gap-1">
                  {mcpServers.map((srv) => {
                    const on = mcpSelected.includes(srv.id);
                    return (
                      <label
                        key={srv.id}
                        className="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-sm transition-colors hover:bg-secondary"
                      >
                        <Checkbox
                          checked={on}
                          onCheckedChange={() =>
                            setMcpSelected((prev) =>
                              on
                                ? prev.filter((id) => id !== srv.id)
                                : [...prev, srv.id],
                            )
                          }
                        />
                        <span className="min-w-0 flex-1 truncate">{srv.name}</span>
                      </label>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            Отмена
          </Button>
          <Button
            onClick={() => void onSubmit()}
            disabled={busy || loading || noAgents || !selectedConnection}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}
            Создать
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
