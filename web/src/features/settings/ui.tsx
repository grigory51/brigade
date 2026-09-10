import { useId, type ComponentType, type ReactNode } from "react";
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
  as: Heading = "h2",
}: {
  title: string;
  badge?: ReactNode;
  children?: ReactNode;
  as?: "h2" | "h3";
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2.5">
        <Heading className="text-[16.5px] font-semibold">{title}</Heading>
        {badge}
      </div>
      {children}
    </div>
  );
}

// SettingsGroup отделяет тематическую группу от заголовка всей страницы настроек.
export function SettingsGroup({ title, icon: Icon, description, badge, children }: {
  title: string;
  icon: ComponentType<{ className?: string }>;
  description?: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-3">
      <header className="flex items-start gap-2.5">
        <span aria-hidden="true" className="mt-0.5 text-primary"><Icon className="size-4" /></span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 id={id} className="text-sm font-semibold">{title}</h3>
            {badge}
          </div>
          {description && <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{description}</p>}
        </div>
      </header>
      <div className="flex min-w-0 flex-col gap-4 rounded-xl border bg-card/40 p-4 @min-[480px]:p-5">
        {children}
      </div>
    </section>
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
    <div className="flex flex-wrap items-center justify-between gap-4 border-t pt-3.5">
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

// Toggle — визуальное состояние строки-кнопки; отдельного вложенного checkbox нет.
export function Toggle({ on }: { on: boolean }) {
  return (
    <span
      className={cn(
        "flex h-5 w-9 shrink-0 items-center rounded-full p-0.5 transition-colors duration-200",
        on ? "bg-primary" : "bg-secondary",
      )}
    >
      <span
        className={cn(
          "size-4 rounded-full bg-white transition-transform duration-200 ease-[cubic-bezier(.25,1,.4,1)]",
          on && "translate-x-4",
        )}
      />
    </span>
  );
}

// errorText вытаскивает человекочитаемое сообщение из ошибки Connect.
export function errorText(err: unknown, fallback: string): string {
  return err instanceof ConnectError ? err.rawMessage : fallback;
}
