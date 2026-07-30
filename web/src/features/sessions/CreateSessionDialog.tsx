import { useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  agentClient,
  authClient,
  mcpClient,
  sessionClient,
} from "@/api/client";
import { AgentType } from "@/api/gen/brigade/v1/agent_pb";
import type { AgentImagesSettings } from "@/api/gen/brigade/v1/auth_pb";
import { McpServer } from "@/api/gen/brigade/v1/mcp_pb";
import { Session, SessionKind } from "@/api/gen/brigade/v1/session_pb";
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
  const [agentId, setAgentId] = useState("");
  const [kind, setKind] = useState<SessionKind>(SessionKind.CLI);
  const [busy, setBusy] = useState(false);
  // tokenSet — задан ли у пользователя токен Claude. ACP-сессия стартует агента сразу
  // (non-interactive), поэтому без токена не поднимется — ACP-опцию дизейблим. CLI
  // же можно авторизовать вручную в терминале (/login), поэтому доступен всегда.
  const [tokenSet, setTokenSet] = useState<boolean | null>(null);
  // MCP-серверы пользователя и выбранный набор. Выбор помнится между запусками: набор
  // инструментов у человека обычно постоянный, отмечать его заново каждый раз незачем.
  const [mcpServers, setMcpServers] = useState<McpServer[]>([]);
  const [mcpSelected, setMcpSelected] = useState<string[]>(loadMcpSelection);
  // Образы контейнера агента: пустой выбор — базовый образ brigade.
  const [images, setImages] = useState<AgentImagesSettings | null>(null);
  const [image, setImage] = useState<string>(
    () => localStorage.getItem(IMAGE_KEY) ?? "",
  );

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
        const first = res.agentTypes[0];
        if (first) setAgentId(first.id);
      })
      .catch(() => {
        if (!cancelled) setAgents([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open, agents]);

  // Статус токена Claude перечитывается при каждом открытии диалога (мог измениться
  // в настройках). Пока грузится — ACP-опция ещё не блокируется (tokenSet === null).
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setTokenSet(null);
    authClient
      .getClaudeSettings({})
      .then((res) => {
        if (!cancelled) setTokenSet(res.tokenSet);
      })
      .catch(() => {
        if (!cancelled) setTokenSet(false);
      });
    return () => {
      cancelled = true;
    };
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

  // Если токен не задан, а выбран ACP (например, статус догрузился после выбора) —
  // откатываем на CLI, чтобы нельзя было создать заведомо нерабочую ACP-сессию.
  useEffect(() => {
    if (tokenSet === false && kind === SessionKind.ACP) {
      setKind(SessionKind.CLI);
    }
  }, [tokenSet, kind]);

  async function onSubmit() {
    if (!agentId) return;
    setBusy(true);
    try {
      saveMcpSelection(mcpSelected);
      localStorage.setItem(IMAGE_KEY, image);
      const res = await sessionClient.create({
        agentType: agentId,
        kind,
        prompt: "",
        mcpServerIds: mcpSelected,
        image,
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

  const loading = agents === null;
  const noAgents = agents !== null && agents.length === 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
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
          <p className="py-6 text-center text-sm text-muted-foreground">
            Нет доступных типов агентов.
          </p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Агент</Label>
              <Select value={agentId} onValueChange={setAgentId}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Выберите агента" />
                </SelectTrigger>
                <SelectContent>
                  {agents!.map((a) => (
                    <SelectItem key={a.id} value={a.id}>
                      {a.name || a.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label>Режим взаимодействия</Label>
              <Select
                value={String(kind)}
                onValueChange={(v) => setKind(Number(v) as SessionKind)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={String(SessionKind.CLI)}>
                    CLI (терминал)
                  </SelectItem>
                  {/* ACP стартует агента сразу и без токена не поднимется; пока
                      статус токена грузится (tokenSet === null) не блокируем. */}
                  <SelectItem
                    value={String(SessionKind.ACP)}
                    disabled={tokenSet === false}
                  >
                    ACP (чат)
                  </SelectItem>
                </SelectContent>
              </Select>
              {tokenSet === false && (
                <p className="text-xs text-muted-foreground">
                  ACP недоступен без токена Claude — задайте его в{" "}
                  <span className="font-medium">Настройки → Claude</span>. CLI
                  можно авторизовать вручную в терминале.
                </p>
              )}
            </div>

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
            disabled={busy || loading || noAgents || !agentId}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}
            Создать
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
