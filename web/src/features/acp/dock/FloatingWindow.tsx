import { useEffect, type RefObject } from "react";
import type {
  ComponentType,
  CSSProperties,
  PropsWithChildren,
  ReactNode,
  Ref,
} from "react";
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
  style,
  ref,
  titlebar,
  children,
}: PropsWithChildren<{
  className?: string;
  style?: CSSProperties;
  ref?: Ref<HTMLDivElement>;
  titlebar: ReactNode;
}>) {
  return (
    <div
      ref={ref}
      style={style}
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

// useDismissOnOutside закрывает справочное окно по Esc и клику мимо — оно ведёт себя как
// всплывающая панель, а не как часть страницы. Чип-переключатель исключён: иначе клик по
// нему закрывал бы окно «мимо» и тут же открывал заново своим onClick.
export function useDismissOnOutside(
  ref: RefObject<HTMLElement | null>,
  onClose: () => void,
) {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null;
      if (ref.current?.contains(target ?? null)) return;
      if (target?.closest("[data-dock-chip]")) return;
      onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [ref, onClose]);
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
