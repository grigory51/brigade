import { useContext, useEffect, useMemo, useState } from "react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import { useAuiState, type ToolCallMessagePartComponent } from "@assistant-ui/react";
import type { A2uiMessage } from "@a2ui/web_core/v0_9";
import { FileArchive, Loader2 } from "lucide-react";
import { bareToolName, PUBLISH_FILE_TOOL_NAME } from "../frontendTools";
import { A2uiContext } from "./context";
import { CARDS_CATALOG_ID } from "./catalog";

const downloadUrlPattern =
  /\/api\/sessions\/[A-Za-z0-9._~-]+\/files\/[^\s)"\\]+/g;

type PublishedFile = { name: string; url: string; previewUrl?: string; key: string };

function publishedFile(result: unknown): PublishedFile | null {
  const text =
    typeof result === "string"
      ? result
      : result == null
        ? ""
        : JSON.stringify(result);
  const urls = text.match(downloadUrlPattern);
  const url = urls?.[0];
  if (!url) return null;
  try {
    const [urlPath] = url.split(/[?#]/);
    return {
      name: decodeURIComponent(urlPath.slice(urlPath.lastIndexOf("/") + 1)),
      url,
      previewUrl: urls?.[1],
      key: urlPath,
    };
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
  const outdated = useAuiState((state) => {
    let currentSeen = false;
    for (const message of state.thread.messages) {
      for (const part of message.content) {
        if (part.type !== "tool-call") continue;
        if (part.toolCallId === props.toolCallId) {
          currentSeen = true;
          continue;
        }
        if (
          currentSeen &&
          file &&
          bareToolName(part.toolName) === PUBLISH_FILE_TOOL_NAME &&
          publishedFile(part.result)?.key === file.key
        ) {
          return true;
        }
      }
    }
    return false;
  });

  useEffect(() => {
    if (!processor || !file || outdated) return;
    const surfaceId = props.toolCallId;
    const messages: A2uiMessage[] = [];
    const component = file.previewUrl
      ? {
          id: "root",
          component: "CadViewer",
          name: { path: "/name" },
          sourceUrl: { path: "/url" },
          previewUrl: { path: "/previewUrl" },
        }
      : /\.(?:glb|gltf)$/i.test(file.name)
        ? {
            id: "root",
            component: "ModelViewer",
            name: { path: "/name" },
            url: { path: "/url" },
          }
        : {
            id: "root",
            component: "DownloadView",
            name: { path: "/name" },
            url: { path: "/url" },
          };
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
          components: [component],
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
  }, [file, outdated, processor, props.toolCallId]);

  if (outdated) {
    return (
      <div className="flex items-center gap-2.5 rounded-lg border bg-card/60 p-3 text-sm text-muted-foreground">
        <FileArchive className="size-4 shrink-0 opacity-70" />
        <span className="min-w-0 truncate">
          Предыдущая версия{file?.name && <span className="text-foreground/70"> · {file.name}</span>}
        </span>
      </div>
    );
  }

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
