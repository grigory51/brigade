import type { ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Loader2, Lock } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * Общие элементы разделов настроек: заголовок, описание, капсула состояния, заметка о
 * судьбе секрета, зона необратимых действий. Живут отдельно от SettingsPage, потому что
 * ими пользуются и разделы, вынесенные в свои файлы (MCP, секреты).
 */

// Badge — капсула состояния у заголовка раздела.
export function Badge({ on, children }: { on: boolean; children: ReactNode }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center rounded-full px-2 text-[10.5px]",
        on ? "bg-success/12 text-[#8dbf82]" : "bg-secondary text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}

// SecretNote — заметка о судьбе секрета. Стоит под своим полем, а не общим дисклеймером
// в подвале: вопрос «а куда денется мой токен» возникает ровно в момент ввода.
export function SecretNote({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-start gap-1.5 text-[11.5px] leading-[1.55] text-[#6c695f]">
      <Lock className="mt-0.5 size-3 shrink-0" />
      <span>{children}</span>
    </p>
  );
}

export function SectionHeader({
  title,
  badge,
  children,
}: {
  title: string;
  badge?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2.5">
        <h2 className="text-[16.5px] font-semibold">{title}</h2>
        {badge}
      </div>
      {children}
    </div>
  );
}

// Code — инлайновый код в описаниях разделов (команды, схемы remote).
export function Code({ children }: { children: ReactNode }) {
  return (
    <code className="rounded-[5px] border bg-[#1c1b1a] px-1.5 py-px font-mono text-[11.5px]">
      {children}
    </code>
  );
}

export function Description({ children }: { children: ReactNode }) {
  return (
    <p className="text-[12.5px] leading-[1.65] text-muted-foreground/70">
      {children}
    </p>
  );
}

export function ExternalLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="text-primary transition-colors hover:text-[#f0a184]"
    >
      {children}
    </a>
  );
}

export function FieldLabel({ children }: { children: ReactNode }) {
  return <span className="text-xs text-muted-foreground">{children}</span>;
}

// DangerZone — отбитый сверху блок необратимых действий.
export function DangerZone({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-4 border-t pt-3.5">
      <div className="min-w-0">
        <div className="text-[13px] text-[#e7e5df]">{title}</div>
        <div className="text-[11.5px] text-[#6c695f]">{hint}</div>
      </div>
      {children}
    </div>
  );
}

export function Loading() {
  return (
    <div className="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 className="size-4 animate-spin" />
      Загрузка…
    </div>
  );
}

// errorText вытаскивает человекочитаемое сообщение из ошибки Connect.
export function errorText(err: unknown, fallback: string): string {
  return err instanceof ConnectError ? err.rawMessage : fallback;
}
