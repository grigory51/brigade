import { useContext, useEffect, useMemo, useState } from "react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import type { ToolCallMessagePartComponent } from "@assistant-ui/react";
import type { A2uiMessage } from "@a2ui/web_core/v0_9";
import { FileArchive, Loader2 } from "lucide-react";
import { A2uiContext } from "./context";
import { CARDS_CATALOG_ID } from "./catalog";

const downloadUrlPattern =
  /\/api\/sessions\/[A-Za-z0-9._~-]+\/files\/[^\s)"\\]+/;

function publishedFile(result: unknown): { name: string; url: string } | null {
  const text =
    typeof result === "string"
      ? result
      : result == null
        ? ""
        : JSON.stringify(result);
  const url = text.match(downloadUrlPattern)?.[0];
  if (!url) return null;
  try {
    return { name: decodeURIComponent(url.slice(url.lastIndexOf("/") + 1)), url };
  } catch {
    return null;
  }
}

// PublishFileCard строит A2UI-поверхность из результата встроенного publish_file.
// Поверхность восстанавливается и из истории, где server→client CUSTOM-события не хранятся.
export const PublishFileCard: ToolCallMessagePartComponent = (props) => {
  const a2ui = useContext(A2uiContext);
  const processor = a2ui?.processor;
  const file = useMemo(() => publishedFile(props.result), [props.result]);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!processor || !file) return;
    const surfaceId = props.toolCallId;
    const messages: A2uiMessage[] = [];
    if (!processor.model.getSurface(surfaceId)) {
      messages.push({
        version: "v0.9",
        createSurface: { surfaceId, catalogId: CARDS_CATALOG_ID },
      } as A2uiMessage);
    }
    messages.push(
      {
        version: "v0.9",
        updateComponents: {
          surfaceId,
          components: [
            {
              id: "root",
              component: "DownloadView",
              name: { path: "/name" },
              url: { path: "/url" },
            },
          ],
        },
      } as A2uiMessage,
      {
        version: "v0.9",
        updateDataModel: { surfaceId, path: "/", value: file },
      } as A2uiMessage,
    );
    try {
      processor.processMessages(messages);
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, [file, processor, props.toolCallId]);

  const surface = processor?.model.surfacesMap.get(props.toolCallId);
  if (surface && !failed) {
    return (
      <div className="brigade-a2ui">
        <A2uiSurface surface={surface} />
      </div>
    );
  }

  const complete = props.status.type === "complete";
  return (
    <div className="flex items-center gap-2 rounded-xl border border-dashed border-border bg-card/40 p-3 text-sm text-muted-foreground">
      <FileArchive className="size-4" />
      {!complete && <Loader2 className="size-3 animate-spin" />}
      <span>{complete ? "Не удалось подготовить файл" : "Готовлю файл…"}</span>
    </div>
  );
};
