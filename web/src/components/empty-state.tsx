import type { ComponentType, ReactNode } from "react";

export function EmptyState({ icon: Icon, title, description, children }: {
  icon: ComponentType<{ className?: string }>;
  title: string;
  description: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="m-auto flex w-full max-w-lg shrink-0 flex-col items-center px-4 py-12 text-center sm:py-20">
      <div aria-hidden="true" className="mb-7 flex size-24 items-center justify-center rounded-full bg-primary/10 text-primary">
        <Icon className="size-11 stroke-[1.25]" />
      </div>
      <h2 className="text-balance font-serif text-2xl leading-tight text-foreground sm:text-3xl">{title}</h2>
      <p className="mt-3 max-w-sm text-pretty text-sm leading-relaxed text-muted-foreground">{description}</p>
      {children && <div className="mt-6 flex flex-wrap items-center justify-center gap-3">{children}</div>}
    </div>
  );
}
