import { createContext, useContext, useRef, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Loader2, Paperclip } from "lucide-react";
import { toast } from "sonner";

import { TooltipIconButton } from "@/components/assistant-ui/tooltip-icon-button";
import { usePendingContext } from "@/components/assistant-ui/composer-context";

// UploadFn заливает файл и возвращает путь, по которому агент прочитает его (относительно
// своей рабочей директории). Реализация — на стороне фичи (сессия знает свой id).
export type UploadFn = (file: File) => Promise<string>;

// ComposerUploadContext прокидывает загрузчик в composer. null — заливка недоступна
// (readonly-лента архива), тогда кнопка не рендерится.
export const ComposerUploadContext = createContext<UploadFn | null>(null);

// ComposerUploadButton — кнопка «приложить файл» в composer. Транспорт AG-UI текстовый, поэтому
// файл заливается в рабочую директорию агента (uploads/<...>), а его путь кладётся в зону
// контекста над инпутом (чипом) и подмешивается в промпт при отправке — агент читает файл сам
// (Read умеет и картинки, и текст). Раньше путь пихался прямо в текст сообщения.
export function ComposerUploadButton() {
  const upload = useContext(ComposerUploadContext);
  const pending = usePendingContext();
  const inputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);

  if (!upload) return null;

  async function onFiles(files: FileList | null) {
    if (!files || files.length === 0 || !upload) return;
    setBusy(true);
    try {
      for (const file of Array.from(files)) {
        const path = await upload(file);
        pending?.add({ kind: "file", path, name: file.name });
      }
    } catch (err) {
      toast.error(
        err instanceof ConnectError ? err.rawMessage : "Не удалось загрузить файл",
      );
    } finally {
      setBusy(false);
      if (inputRef.current) inputRef.current.value = "";
    }
  }

  return (
    <>
      <input
        ref={inputRef}
        type="file"
        multiple
        hidden
        onChange={(e) => void onFiles(e.target.files)}
      />
      <TooltipIconButton
        tooltip="Приложить файл"
        side="bottom"
        type="button"
        variant="ghost"
        size="icon"
        className="size-7 rounded-full"
        aria-label="Приложить файл"
        disabled={busy}
        onClick={() => inputRef.current?.click()}
      >
        {busy ? <Loader2 className="animate-spin" /> : <Paperclip />}
      </TooltipIconButton>
    </>
  );
}
