import { useEffect, useMemo, useState } from "react";
import { Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { agentClient, authClient } from "@/api/client";
import { AgentConnection } from "@/api/gen/brigade/v1/agent_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TerminalOutput } from "@/features/terminal/TerminalView";
import { Badge, DangerZone, Description, FieldLabel, Loading, SecretNote, SectionHeader, errorText } from "./ui";

const emptyConnection = () => new AgentConnection({
  id: "",
  name: "",
  agentType: "claude-code",
  authProfile: "claude-token",
  secretSet: false,
});

export function AgentConnectionsSection({
  connections,
  selectedId,
  onSelect,
  onChange,
}: {
  connections: AgentConnection[] | null;
  selectedId: string;
  onSelect: (id: string) => void;
  onChange: (connections: AgentConnection[]) => void;
}) {
  const selected = useMemo(
    () => connections?.find((connection) => connection.id === selectedId),
    [connections, selectedId],
  );
  const [draft, setDraft] = useState<AgentConnection>(emptyConnection);
  const [secret, setSecret] = useState("");
  const [saving, setSaving] = useState(false);
  const [loginId, setLoginId] = useState("");
  const [loginConnectionId, setLoginConnectionId] = useState("");
  const [loginOutput, setLoginOutput] = useState("");

  useEffect(() => {
    setDraft(selected ? selected.clone() : emptyConnection());
    setSecret("");
    setLoginOutput("");
  }, [selected]);

  useEffect(() => {
    if (!loginId) return;
    const timer = window.setInterval(() => {
      void authClient.getCodexLogin({ id: loginId }).then(async (login) => {
        setLoginOutput(login.output || login.error);
        if (login.status === "completed") {
          window.clearInterval(timer);
          setLoginId("");
          const result = await agentClient.listConnections({});
          onChange(result.connections);
          onSelect(loginConnectionId);
          toast.success("ChatGPT подключён");
        } else if (login.status === "failed") {
          window.clearInterval(timer);
          setLoginId("");
          toast.error(login.error || "Codex login завершился с ошибкой");
        }
      }).catch((error) => {
        window.clearInterval(timer);
        setLoginId("");
        toast.error(errorText(error, "Не удалось получить состояние Codex login"));
      });
    }, 700);
    return () => window.clearInterval(timer);
  }, [loginId, loginConnectionId, onChange, onSelect]);

  if (!connections) return <Loading />;
  const update = (next: Partial<AgentConnection>) => setDraft((current) => Object.assign(current.clone(), next));

  const save = async () => {
    setSaving(true);
    try {
      const saved = await agentClient.saveConnection({ connection: draft, secret: secret.trim() });
      onChange([...connections.filter((connection) => connection.id !== saved.id), saved]);
      onSelect(saved.id);
      setSecret("");
      toast.success("Агент сохранён");
    } catch (error) {
      toast.error(errorText(error, "Не удалось сохранить агента"));
    } finally {
      setSaving(false);
    }
  };

  const startLogin = async () => {
    setSaving(true);
    setLoginOutput("Запускаем Codex device login…\r\n");
    try {
      const login = await agentClient.startCodexLogin({ connection: draft });
      setLoginId(login.id);
      setLoginConnectionId(login.connectionId);
      if (login.output) setLoginOutput(login.output);
    } catch (error) {
      toast.error(errorText(error, "Не удалось запустить Codex login"));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!draft.id) return;
    try {
      await agentClient.deleteConnection({ id: draft.id });
      const next = connections.filter((connection) => connection.id !== draft.id);
      onChange(next);
      onSelect("new");
      toast.success("Агент удалён");
    } catch (error) {
      toast.error(errorText(error, "Не удалось удалить агента"));
    }
  };

  const codex = draft.agentType === "codex";
  const canSave = Boolean(draft.name.trim() && (draft.secretSet || secret.trim()));
  return (
    <>
      <SectionHeader
        title={selected ? selected.name : "Новый агент"}
        badge={selected && <Badge on>подключён</Badge>}
      >
        <Description>Отдельное подключение хранит собственную учётную запись агента. Можно добавить несколько Claude или Codex.</Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        <FieldLabel>Название</FieldLabel>
        <Input value={draft.name} onChange={(event) => update({ name: event.target.value })} placeholder={codex ? "Рабочий Codex" : "Личный Claude"} />
      </div>

      <div className="grid grid-cols-2 gap-3.5">
        <div className="flex flex-col gap-2">
          <FieldLabel>Агент</FieldLabel>
          <Select disabled={Boolean(selected)} value={draft.agentType} onValueChange={(agentType) => update({ agentType, authProfile: agentType === "codex" ? "chatgpt" : "claude-token" })}>
            <SelectTrigger className="h-[41px] w-full"><SelectValue /></SelectTrigger>
            <SelectContent><SelectItem value="claude-code">Claude Code</SelectItem><SelectItem value="codex">Codex</SelectItem></SelectContent>
          </Select>
        </div>
        {codex && (
          <div className="flex flex-col gap-2">
            <FieldLabel>Авторизация</FieldLabel>
            <Select disabled={Boolean(selected)} value={draft.authProfile} onValueChange={(authProfile) => update({ authProfile })}>
              <SelectTrigger className="h-[41px] w-full"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="chatgpt">ChatGPT Plus</SelectItem><SelectItem value="api-key">OpenAI API Key</SelectItem></SelectContent>
            </Select>
          </div>
        )}
      </div>

      {codex && draft.authProfile === "chatgpt" && (
        <div className="flex flex-col gap-2">
          <FieldLabel>ChatGPT Plus</FieldLabel>
          <Button className="self-start" disabled={saving || Boolean(loginId) || !draft.name.trim()} onClick={() => void startLogin()}>
            {(saving || loginId) && <Loader2 className="size-4 animate-spin" />} Подключить ChatGPT
          </Button>
          {loginOutput && <TerminalOutput content={loginOutput} />}
          <Description>Либо импортируйте <code>~/.codex/auth.json</code> с доверенной машины.</Description>
          <textarea value={secret} onChange={(event) => setSecret(event.target.value)} placeholder={'{"auth_mode":"chatgpt",…}'} className="min-h-24 rounded-md border bg-[#1c1b1a] px-3 py-2 font-mono text-xs outline-none" spellCheck={false} />
        </div>
      )}

      {(!codex || draft.authProfile === "api-key") && (
        <div className="flex flex-col gap-2">
          <FieldLabel>{codex ? "OpenAI API Key" : "Подписочный токен Claude Code"}</FieldLabel>
          <Input type="password" value={secret} onChange={(event) => setSecret(event.target.value)} placeholder={draft.secretSet ? "Пусто — не менять" : codex ? "sk-…" : "sk-ant-oat01-…"} autoComplete="off" />
        </div>
      )}
      <SecretNote>Секрет шифруется на сервере и никогда не возвращается в API</SecretNote>

      <Button className="self-start" disabled={saving || !canSave} onClick={() => void save()}>
        {saving && <Loader2 className="size-4 animate-spin" />} Сохранить
      </Button>

      {selected && <DangerZone title="Удалить агента" hint="Связанные сессии и Telegram-боты больше не смогут запускать агента"><Button variant="ghost" className="text-destructive" onClick={() => void remove()}><Trash2 className="size-4" /> Удалить</Button></DangerZone>}
    </>
  );
}
