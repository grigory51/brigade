import { useEffect, useState } from "react";
import { Check, ChevronsUpDown, Plus, Server } from "lucide-react";
import { desktopClient } from "@/api/client";
import type { DesktopEnvironment } from "@/api/gen/brigade/v1/desktop_pb";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { toast } from "sonner";

export function EnvironmentSwitcher() {
  const [environments, setEnvironments] = useState<DesktopEnvironment[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    void desktopClient.listEnvironments({})
      .then((result) => setEnvironments(result.environments))
      .catch(() => setEnvironments(null));
  }, []);

  if (!environments) return null;
  const active = environments.find((environment) => environment.active) ?? environments[0];

  const select = async (environment: DesktopEnvironment) => {
    if (environment.active) return;
    await desktopClient.selectEnvironment({ id: environment.id });
    window.location.assign(environment.connected ? "/sessions" : "/login");
  };

  const add = async () => {
    setSaving(true);
    try {
      const environment = await desktopClient.addEnvironment({ name, baseUrl });
      await desktopClient.selectEnvironment({ id: environment.id });
      window.location.assign("/login");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Не удалось добавить окружение");
      setSaving(false);
    }
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button className="flex h-8 w-full items-center gap-2 rounded-lg border border-sidebar-border bg-sidebar-accent/40 px-2 text-xs text-sidebar-foreground hover:bg-sidebar-accent group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
            <Server className="size-3.5 shrink-0" />
            <span className="min-w-0 flex-1 truncate text-left group-data-[collapsible=icon]:hidden">{active?.name}</span>
            <span className={`size-1.5 rounded-full group-data-[collapsible=icon]:hidden ${active?.connected ? "bg-success" : "bg-destructive"}`} />
            <ChevronsUpDown className="size-3 text-muted-foreground group-data-[collapsible=icon]:hidden" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-60">
          {environments.map((environment) => (
            <DropdownMenuItem key={environment.id} onClick={() => void select(environment)}>
              <Server className="size-3.5" />
              <span className="min-w-0 flex-1 truncate">{environment.name}</span>
              {environment.active && <Check className="size-3.5" />}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setAdding(true)}>
            <Plus className="size-3.5" /> Добавить окружение
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Удалённое окружение</DialogTitle>
            <DialogDescription>Подключение к существующему инстансу Brigade.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="Название, например Production" />
            <Input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://brigade.example.com" />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAdding(false)}>Отмена</Button>
            <Button disabled={saving || !baseUrl.trim()} onClick={() => void add()}>Добавить</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
