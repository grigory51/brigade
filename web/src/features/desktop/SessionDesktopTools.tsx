import { useEffect, useState } from "react";
import { Folder, FolderOpen, Loader2, Network, Plus, X } from "lucide-react";
import { desktopClient } from "@/api/client";
import type { DesktopEnvironment, DesktopPortForward } from "@/api/gen/brigade/v1/desktop_pb";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

declare global { interface Window { brigadeRevealFile?: (path: string) => void } }

const chipClass = "inline-flex h-[30px] items-center gap-1.5 rounded-full border border-[rgba(65,63,59,0.9)] bg-[rgba(31,30,29,0.85)] px-3 text-xs text-muted-foreground shadow-[0_4px_14px_rgba(0,0,0,0.3)] backdrop-blur-[8px] transition-colors hover:border-[#5a4034] hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50";

export function SessionDesktopTools({ sessionId, sessionName }: { sessionId: string; sessionName: string }) {
  const [environment, setEnvironment] = useState<DesktopEnvironment | null>(null);
  const [portsOpen, setPortsOpen] = useState(false);
  const [remotePort, setRemotePort] = useState("");
  const [localPort, setLocalPort] = useState("");
  const [busy, setBusy] = useState(false);

  const load = () => desktopClient.listEnvironments({}).then((result) => setEnvironment(result.environments.find((item) => item.active && item.kind === "remote") ?? null)).catch(() => setEnvironment(null));
  useEffect(() => { void load(); }, [sessionId]);
  if (!environment) return null;

  const canMount = environment.capabilities.includes("workspace-rw-v1");
  const canForward = environment.capabilities.includes("tcp-tunnel-v1");
  const unavailable = "Обновите Brigade на удалённом инстансе";
  const mount = environment.mounts.find((item) => item.sessionId === sessionId);
  const forwards = environment.portForwards.filter((item) => item.sessionId === sessionId);

  const mountWorkspace = async () => {
    setBusy(true);
    try { const result = await desktopClient.mountWorkspace({ sessionId, sessionName }); await load(); window.brigadeRevealFile?.(result.path); }
    catch (error) { toast.error(error instanceof Error ? error.message : "Не удалось смонтировать workspace"); }
    finally { setBusy(false); }
  };

  const addForward = async () => {
    setBusy(true);
    try {
      await desktopClient.addPortForward({ sessionId, remotePort: Number(remotePort), localPort: localPort ? Number(localPort) : 0 });
      setRemotePort(""); setLocalPort(""); await load();
    } catch (error) { toast.error(error instanceof Error ? error.message : "Не удалось пробросить порт"); }
    finally { setBusy(false); }
  };

  return (
    <>
      {canMount && (mount ? (
        <div className="flex h-[30px] items-center rounded-full border border-[#5a4034] bg-[rgba(31,30,29,0.85)] text-xs text-foreground shadow-[0_4px_14px_rgba(0,0,0,0.3)] backdrop-blur-[8px]">
          <button type="button" data-dock-chip className="inline-flex h-full items-center gap-1.5 rounded-l-full px-3" onClick={() => window.brigadeRevealFile?.(mount.path)}><FolderOpen className="size-3.5" /><span className="hidden sm:inline">Файлы</span></button>
          <button type="button" data-dock-chip className="flex h-full w-7 items-center justify-center rounded-r-full border-l border-[#5a4034] text-muted-foreground hover:text-foreground" aria-label="Отключить файлы" onClick={async () => { await desktopClient.unmountWorkspace({ id: mount.id }); await load(); }}><X className="size-3" /></button>
        </div>
      ) : (
        <button type="button" data-dock-chip className={chipClass} disabled={busy} onClick={() => void mountWorkspace()}>{busy ? <Loader2 className="size-3.5 animate-spin" /> : <Folder className="size-3.5" />}<span className="hidden sm:inline">Файлы</span></button>
      ))}
      {!canMount && <button type="button" data-dock-chip className={chipClass} disabled title={unavailable}><Folder className="size-3.5" /><span className="hidden sm:inline">Файлы</span></button>}
      <button type="button" data-dock-chip className={cn(chipClass, forwards.length && "border-[#5a4034] text-foreground")} disabled={!canForward} title={canForward ? undefined : unavailable} onClick={() => setPortsOpen(true)}><Network className="size-3.5" /><span className="hidden sm:inline">Порты</span>{forwards.length ? ` · ${forwards.length}` : ""}</button>
      <Dialog open={portsOpen} onOpenChange={setPortsOpen}>
        <DialogContent>
          <DialogHeader><DialogTitle>Проброс портов</DialogTitle></DialogHeader>
          <div className="space-y-2">
            {forwards.map((forward: DesktopPortForward) => (
              <div key={forward.id} className="flex items-center gap-3 rounded-lg border px-3 py-2 text-sm">
                <span className={`size-1.5 rounded-full ${forward.status === "error" ? "bg-destructive" : "bg-success"}`} />
                <a className="flex-1 font-mono text-primary" href={`http://127.0.0.1:${forward.localPort}`} target="_blank" rel="noreferrer">localhost:{forward.localPort} → {forward.remotePort}</a>
                <Button size="icon" variant="ghost" className="size-7" onClick={async () => { await desktopClient.removePortForward({ id: forward.id }); await load(); }}><X className="size-3.5" /></Button>
              </div>
            ))}
            <div className="grid grid-cols-[1fr_1fr_auto] gap-2 pt-2">
              <Input inputMode="numeric" value={remotePort} onChange={(event) => setRemotePort(event.target.value)} placeholder="Порт в сессии" />
              <Input inputMode="numeric" value={localPort} onChange={(event) => setLocalPort(event.target.value)} placeholder="Локальный (авто)" />
              <Button size="icon" disabled={busy || !remotePort} onClick={() => void addForward()}><Plus className="size-4" /></Button>
            </div>
          </div>
          <DialogFooter><Button variant="outline" onClick={() => setPortsOpen(false)}>Готово</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
