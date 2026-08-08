import { useEffect, useState } from "react";
import { Laptop, Server } from "lucide-react";
import { desktopClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

export function DesktopSetup() {
  const [open, setOpen] = useState(false);
  const [remote, setRemote] = useState(false);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [localId, setLocalId] = useState("local");

  useEffect(() => {
    void desktopClient.listEnvironments({}).then((state) => {
      setOpen(state.needsSetup);
      setLocalId(state.environments.find((environment) => environment.kind === "local")?.id ?? "local");
    }).catch(() => undefined);
  }, []);

  const selectLocal = async () => { await desktopClient.selectEnvironment({ id: localId }); setOpen(false); };
  const addRemote = async () => {
    const environment = await desktopClient.addEnvironment({ name, baseUrl });
    await desktopClient.selectEnvironment({ id: environment.id });
    window.location.assign("/login");
  };

  return (
    <Dialog open={open}>
      <DialogContent showCloseButton={false} onEscapeKeyDown={(event) => event.preventDefault()} onPointerDownOutside={(event) => event.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Где находится Brigade?</DialogTitle>
          <DialogDescription>Окружение определяет сессии, настройки и рабочие файлы в этом окне.</DialogDescription>
        </DialogHeader>
        {!remote ? (
          <div className="grid grid-cols-2 gap-3">
            <button onClick={() => void selectLocal()} className="flex flex-col items-start gap-2 rounded-xl border p-4 text-left hover:bg-accent"><Laptop className="size-5" /><strong>На этом Mac</strong><span className="text-xs text-muted-foreground">Текущее локальное окружение.</span></button>
            <button onClick={() => setRemote(true)} className="flex flex-col items-start gap-2 rounded-xl border p-4 text-left hover:bg-accent"><Server className="size-5" /><strong>Удалённое</strong><span className="text-xs text-muted-foreground">Подключиться к Brigade по HTTPS.</span></button>
          </div>
        ) : (
          <div className="space-y-3">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="Название" autoFocus />
            <Input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://brigade.example.com" />
            <div className="flex justify-end gap-2"><Button variant="outline" onClick={() => setRemote(false)}>Назад</Button><Button disabled={!baseUrl.trim()} onClick={() => void addRemote()}>Продолжить</Button></div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
