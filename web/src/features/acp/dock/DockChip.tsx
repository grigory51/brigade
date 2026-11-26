import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function DockChip({ active = false, className, ...props }: ComponentProps<"button"> & { active?: boolean }) {
  return (
    <button
      type="button"
      data-dock-chip
      className={cn(
        "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-full border bg-[rgba(31,30,29,0.85)] px-3 py-1.5 text-xs shadow-[0_4px_14px_rgba(0,0,0,0.3)] backdrop-blur-[8px] transition-colors hover:border-[#5a4034] hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        active ? "border-[#5a4034] text-foreground" : "border-[rgba(65,63,59,0.9)] text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}
