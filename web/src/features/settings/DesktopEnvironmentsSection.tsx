import { useEffect, useState } from "react";
import { Check, Loader2, LogIn, LogOut, Pencil, Plus, Server, Trash2 } from "lucide-react";
import { desktopClient } from "@/api/client";
import type { DesktopEnvironment } from "@/api/gen/brigade/v1/desktop_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "sonner";
import { Description, SectionHeader } from "./ui";

export function DesktopEnvironmentsSection() {
  const [environments, setEnvironments] = useState<DesktopEnvironment[]>([]);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [loginId, setLoginId] = useState("");
  const [editId, setEditId] = useState("");
  const [editName, setEditName] = useState("");
  const [editUrl, setEditUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const load = () => desktopClient.listEnvironments({}).then((result) => setEnvironments(result.environments));
  useEffect(() => { void load(); }, []);

  const add = async () => {
    setBusy(true);
    try {
      await desktopClient.addEnvironment({ name, baseUrl });
      setName(""); setBaseUrl(""); await load();
    } catch (error) { toast.error(error instanceof Error ? error.message : "Не удалось добавить окружение"); }
    finally { setBusy(false); }
  };

  const login = async () => {
    setBusy(true);
    try {
      await desktopClient.loginEnvironment({ id: loginId, username, password });
      setLoginId(""); setPassword(""); await load();
    } catch { toast.error("Не удалось войти в окружение"); }
    finally { setBusy(false); }
  };

  const select = async (id: string) => {
    await desktopClient.selectEnvironment({ id });
    window.location.assign("/sessions");
  };

  const update = async () => {
    setBusy(true);
    try {
      await desktopClient.updateEnvironment({ id: editId, name: editName, baseUrl: editUrl });
      setEditId(""); await load();
    } catch (error) { toast.error(error instanceof Error ? error.message : "Не удалось изменить окружение"); }
    finally { setBusy(false); }
  };

  return (
    <>
      <SectionHeader title="Окружения">
        <Description>Brigade.app может работать с локальным или удалёнными инстансами. Активное окружение определяет все сессии и настройки в окне.</Description>
      </SectionHeader>
      <div className="space-y-2">
        {environments.map((environment) => (
          <div key={environment.id} className="rounded-xl border bg-card/40 p-3">
            <div className="flex items-center gap-3">
              <Server className="size-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-sm font-medium">{environment.name}{environment.active && <Check className="size-3.5 text-success" />}</div>
                <div className="truncate text-xs text-muted-foreground">{environment.kind === "local" ? "На этом Mac" : environment.baseUrl}</div>
              </div>
              {environment.kind === "remote" && !environment.connected && (
                <Button size="sm" variant="outline" onClick={() => setLoginId(environment.id)}><LogIn className="size-3.5" /> Войти</Button>
              )}
              {!environment.active && environment.connected && <Button size="sm" variant="outline" onClick={() => void select(environment.id)}>Выбрать</Button>}
              {environment.kind === "remote" && (
                <>
                  {environment.connected && <Button size="icon" variant="ghost" aria-label="Выйти" onClick={async () => { await desktopClient.logoutEnvironment({ id: environment.id }); environment.active ? window.location.assign("/login") : await load(); }}><LogOut className="size-4" /></Button>}
                  <Button size="icon" variant="ghost" aria-label="Изменить окружение" onClick={() => { setEditId(environment.id); setEditName(environment.name); setEditUrl(environment.baseUrl); }}><Pencil className="size-4" /></Button>
                  <Button size="icon" variant="ghost" aria-label="Удалить окружение" onClick={async () => { await desktopClient.deleteEnvironment({ id: environment.id }); await load(); }}><Trash2 className="size-4" /></Button>
                </>
              )}
            </div>
            {loginId === environment.id && (
              <div className="mt-3 grid grid-cols-2 gap-2 border-t pt-3">
                <Input value={username} onChange={(event) => setUsername(event.target.value)} placeholder="Логин" />
                <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="Пароль" />
                <Button className="col-span-2" disabled={busy || !username || !password} onClick={() => void login()}>{busy ? <Loader2 className="size-4 animate-spin" /> : "Подключить"}</Button>
              </div>
            )}
            {editId === environment.id && (
              <div className="mt-3 grid gap-2 border-t pt-3">
                <Input value={editName} onChange={(event) => setEditName(event.target.value)} placeholder="Название" />
                <Input value={editUrl} onChange={(event) => setEditUrl(event.target.value)} placeholder="https://brigade.example.com" />
                <div className="flex justify-end gap-2"><Button variant="outline" onClick={() => setEditId("")}>Отмена</Button><Button disabled={busy || !editUrl.trim()} onClick={() => void update()}>Сохранить</Button></div>
              </div>
            )}
          </div>
        ))}
      </div>
      <div className="space-y-3 rounded-xl border border-dashed p-4">
        <div className="flex items-center gap-2 text-sm font-medium"><Plus className="size-4" />Удалённое окружение</div>
        <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="Название" />
        <Input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://brigade.example.com" />
        <Button disabled={busy || !baseUrl.trim()} onClick={() => void add()}>Добавить</Button>
      </div>
    </>
  );
}
