import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Sidebar,
  SidebarHeader,
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { EnvironmentSwitcher } from "./EnvironmentSwitcher";

export function DesktopLoginShell({ children }: { children: ReactNode }) {
  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader className="gap-3">
          <div className="flex items-center justify-between gap-2 group-data-[collapsible=icon]:justify-center">
            <Link to="/login" className="flex shrink-0 items-center gap-2 px-1 font-semibold">
              <img src="/logo.svg" alt="" className="size-[26px] shrink-0 rounded-[7px]" />
              <span className="group-data-[collapsible=icon]:hidden">brigade</span>
            </Link>
            <SidebarTrigger className="group-data-[collapsible=icon]:hidden" />
          </div>
          <EnvironmentSwitcher />
          <Button disabled className="h-auto justify-start gap-2 rounded-[9px] py-[9px] text-[13px] group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
            <Plus className="size-4 shrink-0" />
            <span className="group-data-[collapsible=icon]:hidden">Новая сессия</span>
          </Button>
        </SidebarHeader>
      </Sidebar>
      <SidebarInset className="h-svh min-h-0">{children}</SidebarInset>
    </SidebarProvider>
  );
}
