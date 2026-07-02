import { useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { agentClient, authClient, sessionClient } from "@/api/client";
import { AgentType } from "@/api/gen/brigade/v1/agent_pb";
import { Session, SessionKind } from "@/api/gen/brigade/v1/session_pb";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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
  const [cwd, setCwd] = useState("");
  const [busy, setBusy] = useState(false);
  // tokenSet — задан ли у пользователя токен Claude. ACP-сессия стартует агента сразу
  // (non-interactive), поэтому без токена не поднимется — ACP-опцию дизейблим. CLI
  // же можно авторизовать вручную в терминале (/login), поэтому доступен всегда.
  const [tokenSet, setTokenSet] = useState<boolean | null>(null);

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
      const res = await sessionClient.create({
        agentType: agentId,
        kind,
        prompt: "",
        cwd: cwd.trim(),
      });
      const session = res.session;
      if (!session) throw new Error("пустой ответ Create");
      onOpenChange(false);
      resetTransient();
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

  function resetTransient() {
    setCwd("");
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

            <div className="space-y-2">
              <Label htmlFor="cwd">
                Рабочая директория{" "}
                <span className="text-muted-foreground">(опц.)</span>
              </Label>
              <Input
                id="cwd"
                placeholder="по умолчанию из конфига"
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
              />
            </div>
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
