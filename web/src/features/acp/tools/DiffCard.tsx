import { FileDiff } from "lucide-react";
import { cn } from "@/lib/utils";
import { lineDiff, type DiffBlock } from "./diff";

// DiffCard — правка файла (Edit/Write): путь и построчный diff со стандартной
// разметкой (удалённые/добавленные строки, усечённый контекст вокруг).
export function DiffCard({ blocks }: { blocks: DiffBlock[] }) {
  return (
    <div className="space-y-2">
      {blocks.map((b, i) => (
        <FileDiffBlock key={i} block={b} />
      ))}
    </div>
  );
}

// FileDiffBlock экспортируется отдельно: A2UI-каталог (a2ui/catalog.tsx) реализует
// компонент DiffView этим же рендером — одна реализация на оба пути доставки.
export function FileDiffBlock({ block }: { block: DiffBlock }) {
  const lines = lineDiff(block.oldText, block.newText);
  const sep = block.path.lastIndexOf("/");
  const dir = sep > 0 ? block.path.slice(0, sep + 1) : "";
  const base = sep >= 0 ? block.path.slice(sep + 1) : block.path;

  return (
    <div className="overflow-hidden rounded-lg border bg-card/60">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs">
        <FileDiff className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate font-mono">
          <span className="text-muted-foreground">{dir}</span>
          <span className="font-medium">{base}</span>
        </span>
      </div>
      <div className="overflow-x-auto font-mono text-xs leading-relaxed">
        <table className="w-full border-collapse">
          <tbody>
            {lines.map((l, i) => (
              <tr
                key={i}
                className={cn(
                  l.kind === "del" && "bg-destructive/10 text-destructive",
                  l.kind === "add" && "bg-success/10 text-success",
                )}
              >
                <td className="w-8 select-none px-2 text-right text-muted-foreground/60">
                  {l.oldNo ?? ""}
                </td>
                <td className="w-8 select-none pr-2 text-right text-muted-foreground/60">
                  {l.newNo ?? ""}
                </td>
                <td className="w-4 select-none text-center opacity-70">
                  {l.kind === "del" ? "−" : l.kind === "add" ? "+" : ""}
                </td>
                <td className="whitespace-pre-wrap break-all pr-3">{l.text}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
