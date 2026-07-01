import { Circle, CircleCheck, CircleDot, ListTodo } from "lucide-react";
import { cn } from "@/lib/utils";

// PlanEntry — пункт плана агента (ACP plan → STATE_SNAPSHOT {plan: [...]}).
export type PlanEntry = {
  content: string;
  status: "pending" | "in_progress" | "completed" | string;
  priority?: string;
};

// PlanPanel — компактный план агента над composer'ом: агент присылает снимок плана
// целиком при каждом изменении, панель отражает актуальное состояние задач turn'а.
export function PlanPanel({ plan }: { plan: PlanEntry[] }) {
  if (plan.length === 0) return null;
  const done = plan.filter((e) => e.status === "completed").length;

  return (
    <details
      className="group rounded-lg border bg-card/60"
      open={done < plan.length || undefined}
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-1.5 text-xs select-none">
        <ListTodo className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">План</span>
        <span className="text-muted-foreground">
          {done}/{plan.length}
        </span>
      </summary>
      <ul className="space-y-1 border-t px-3 py-2">
        {plan.map((e, i) => (
          <li key={i} className="flex items-start gap-2 text-xs">
            <StatusIcon status={e.status} />
            <span
              className={cn(
                "min-w-0 break-words",
                e.status === "completed" &&
                  "text-muted-foreground line-through",
              )}
            >
              {e.content}
            </span>
          </li>
        ))}
      </ul>
    </details>
  );
}

function StatusIcon({ status }: { status: string }) {
  if (status === "completed") {
    return <CircleCheck className="mt-0.5 size-3.5 shrink-0 text-success" />;
  }
  if (status === "in_progress") {
    return (
      <CircleDot className="mt-0.5 size-3.5 shrink-0 animate-pulse text-primary" />
    );
  }
  return <Circle className="mt-0.5 size-3.5 shrink-0 text-muted-foreground/50" />;
}
