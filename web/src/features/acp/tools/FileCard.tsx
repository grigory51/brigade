import { FileText, Loader2 } from "lucide-react";

// FileCard — чтение файла (Read): содержимое с номерами строк. Адаптер отдаёт текст
// в формате cat -n («N\tтекст»), номера выносятся в отдельную колонку.
export function FileCard({
  content,
  running,
}: {
  content: string | null;
  running: boolean;
}) {
  const rows = content !== null ? parseNumbered(content) : null;

  return (
    <div className="overflow-hidden rounded-lg border bg-card/60">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs">
        <FileText className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">Read File</span>
        {running && (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}
      </div>
      {rows && (
        <div className="max-h-72 overflow-auto font-mono text-xs leading-relaxed">
          <table className="w-full border-collapse">
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  <td className="w-10 select-none px-2 text-right text-muted-foreground/60">
                    {r.no}
                  </td>
                  <td className="whitespace-pre-wrap break-all pr-3">
                    {r.text}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// parseNumbered разбирает строки формата «номер\tтекст»; строки без номера
// показываются как есть (номер пустой).
function parseNumbered(content: string): { no: string; text: string }[] {
  return content.split("\n").map((line) => {
    const m = /^\s*(\d+)\t(.*)$/.exec(line);
    return m ? { no: m[1], text: m[2] } : { no: "", text: line };
  });
}
