import type { ComponentType, PropsWithChildren, ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * FloatingWindow — каркас плавающего окна страницы сессии (ссылки, терминал):
 * стеклянная подложка, тень, анимация появления. Позиция и размер задаются
 * className вызывающего, тайтлбар передаётся слотом — у терминала он свой
 * (macOS-светофор), у остальных окон — WindowTitlebar ниже.
 */
export function FloatingWindow({
  className,
  titlebar,
  children,
}: PropsWithChildren<{ className?: string; titlebar: ReactNode }>) {
  return (
    <div
      className={cn(
        "absolute flex flex-col overflow-hidden rounded-[14px] border border-[#4a4843] bg-[rgba(38,38,36,0.97)] shadow-[0_26px_64px_rgba(0,0,0,0.55)] backdrop-blur-[10px]",
        "animate-[win-in_0.22s_cubic-bezier(0.2,0.8,0.2,1)]",
        className,
      )}
    >
      {titlebar}
      <div className="flex min-h-0 flex-1 flex-col">{children}</div>
    </div>
  );
}

// WindowTitlebar — стандартная шапка окна: терракотовая иконка, заголовок,
// приглушённая подпись справа и крестик закрытия.
export function WindowTitlebar({
  icon: Icon,
  title,
  hint,
  onClose,
}: {
  icon: ComponentType<{ className?: string }>;
  title: string;
  hint?: string;
  onClose: () => void;
}) {
  return (
    <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
      <Icon className="size-3.5 shrink-0 text-primary" />
      <span className="flex-1 text-[12.5px] font-semibold">{title}</span>
      {hint && <span className="text-[11px] text-muted-foreground/70">{hint}</span>}
      <button
        type="button"
        onClick={onClose}
        aria-label="Закрыть"
        className="flex size-5 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-secondary hover:text-foreground"
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}
