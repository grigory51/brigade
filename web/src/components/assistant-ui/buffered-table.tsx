import type { ComponentProps } from "react";
import type { Element, Root } from "hast";
import { useMessagePartText } from "@assistant-ui/react";
import { Loader2, Table2 } from "lucide-react";
import { cn } from "@/lib/utils";

// Анализируем дерево после GFM-парсера: код с символами | не является таблицей.
// Длина относится к уже отображаемому тексту, который может отставать от потока.
export function rehypeTableBuffer() {
  return (tree: Root, file: { value: unknown }) => {
    const source = String(file.value);
    function markTables(parent: Root | Element, followed: boolean) {
      parent.children.forEach((node, index) => {
        if (node.type !== "element") return;
        const hasFollowingBlock = followed || parent.children.slice(index + 1).some(
          (next) => next.type === "element" || (next.type === "text" && Boolean(next.value.trim())),
        );
        if (node.tagName === "table") {
          const end = node.position?.end.offset;
          // Пустая строка закрывает таблицу, в том числе внутри blockquote.
          const closed = hasFollowingBlock || (end !== undefined && /\r?\n[ \t>]*\r?\n/.test(source.slice(end)));
          node.properties["data-table-open"] = !closed;
          node.properties["data-source-length"] = source.length;
        } else {
          markTables(node, hasFollowingBlock);
        }
      });
    }
    markTables(tree, false);
  };
}

type BufferedTableProps = ComponentProps<"table"> & {
  "data-table-open"?: boolean;
  "data-source-length"?: number;
};

export function BufferedTable({
  "data-table-open": open,
  "data-source-length": sourceLength = 0,
  className,
  ...props
}: BufferedTableProps) {
  const { text, status } = useMessagePartText();
  // После Stop/завершения ждём также отложенный markdown-рендер и smooth streaming,
  // иначе на один кадр могла бы появиться устаревшая, частичная таблица.
  if (open && (status.type === "running" || sourceLength < text.length)) {
    return (
      <div role="status" className="my-3 flex h-16 items-center gap-3 rounded-lg border bg-muted/40 px-4 font-sans text-sm text-muted-foreground">
        <Table2 aria-hidden="true" className="size-5 shrink-0" />
        <span>Формируется таблица…</span>
        <Loader2 aria-hidden="true" className="ml-auto size-4 shrink-0 animate-spin motion-reduce:animate-none" />
      </div>
    );
  }
  return (
    <div className="my-3 max-w-full overflow-x-auto">
      <table className={cn("aui-md-table w-full border-separate border-spacing-0", className)} {...props} />
    </div>
  );
}
