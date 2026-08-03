import { createElement, useEffect, useRef, useState } from "react";
import { z } from "zod";
import { Catalog, DynamicValueSchema } from "@a2ui/web_core/v0_9";
import {
  basicCatalog,
  createComponentImplementation,
  type ReactComponentImplementation,
} from "@a2ui/react/v0_9";
import { Box, Download, FileArchive, Loader2, Maximize2 } from "lucide-react";
import { FileDiffBlock } from "../tools/DiffCard";

// basicCatalog — стандартный каталог A2UI (18 компонентов: Text/Card/Column/Row/Button/
// TextField/… с интерактивностью из коробки). Используется generative-UI от агента
// (frontend-tool render_ui): поверхность создаётся с этим каталогом (BASIC_CATALOG_ID),
// компоненты рисуются нативно. Подключается к MessageProcessor рядом с cardsCatalog
// (см. useAcpRuntime); поверхности разных каталогов не пересекаются по catalogId.
export { basicCatalog };
export const BASIC_CATALOG_ID =
  "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json";

// Каталог карточек brigade для A2UI-рендера. Идентификатор согласован с бэкендом
// (backend/internal/a2ui.CardsCatalogID): сервер описывает поверхность с этим
// каталогом, платформенные рендереры (web — этот файл, mobile — Compose-реализация)
// отрисовывают одни и те же компоненты нативно.
export const CARDS_CATALOG_ID = "https://brigade.dev/a2ui/catalogs/cards/v1";

// DiffView — карточка правки файла. Свойство diffs — массив {path, oldText, newText};
// DynamicValueSchema допускает и литеральный массив, и биндинг {path: "/diffs"} в
// модель данных поверхности (бэкенд шлёт биндинг — см. a2ui.DiffSurface).
const DiffViewApi = {
  name: "DiffView",
  schema: z.object({
    diffs: DynamicValueSchema,
  }),
};

type DiffItem = { path?: string; oldText?: string; newText?: string };

const DiffView = createComponentImplementation(DiffViewApi, ({ props }) => {
  const diffs = Array.isArray(props.diffs) ? (props.diffs as DiffItem[]) : [];
  return (
    // data-a2ui маркирует рендер через A2UI-каталог (отличим от React-фолбэка при
    // отладке: реализация карточки общая — FileDiffBlock).
    <div className="space-y-2" data-a2ui="DiffView">
      {diffs.map((d, i) => (
        <FileDiffBlock
          key={i}
          block={{
            path: d.path ?? "",
            oldText: d.oldText ?? "",
            newText: d.newText ?? "",
          }}
        />
      ))}
    </div>
  );
});

const DownloadViewApi = {
  name: "DownloadView",
  schema: z.object({
    name: DynamicValueSchema,
    url: DynamicValueSchema,
  }),
};

const DownloadView = createComponentImplementation(
  DownloadViewApi,
  ({ props }) => {
    const name = typeof props.name === "string" ? props.name : "Файл";
    const url = typeof props.url === "string" ? props.url : "";
    return (
      <a
        href={url}
        download={name}
        className="group flex items-center gap-3 rounded-2xl border border-border bg-card px-4 py-3.5 shadow-[0_14px_38px_rgba(0,0,0,.18)] transition-colors hover:border-primary/50 hover:bg-accent/35"
        data-a2ui="DownloadView"
      >
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
          <FileArchive className="size-5" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-xs text-muted-foreground">
            Файл готов
          </span>
          <span className="block truncate text-sm font-medium text-foreground">
            {name}
          </span>
        </span>
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors group-hover:border-primary/40 group-hover:bg-primary group-hover:text-primary-foreground">
          <Download className="size-4" />
        </span>
      </a>
    );
  },
);

const ModelViewerApi = {
  name: "ModelViewer",
  schema: z.object({
    name: DynamicValueSchema,
    url: DynamicValueSchema,
  }),
};

const CadViewerApi = {
  name: "CadViewer",
  schema: z.object({
    name: DynamicValueSchema,
    sourceUrl: DynamicValueSchema,
    previewUrl: DynamicValueSchema,
  }),
};

function inlineUrl(url: string): string {
  return url + (url.includes("?") ? "&" : "?") + "inline=1";
}

function ArtifactViewer({
  name,
  previewUrl,
  downloadUrl,
  cad,
}: {
  name: string;
  previewUrl: string;
  downloadUrl: string;
  cad: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const [viewerState, setViewerState] = useState<"loading" | "ready" | "error">("loading");

  useEffect(() => {
    let active = true;
    import("@google/model-viewer").then(
      () => active && setViewerState("ready"),
      () => active && setViewerState("error"),
    );
    return () => {
      active = false;
    };
  }, []);

  return (
    <div
      ref={root}
      className="overflow-hidden rounded-2xl border border-border bg-card shadow-[0_18px_48px_rgba(0,0,0,.24)] fullscreen:flex fullscreen:h-screen fullscreen:flex-col fullscreen:rounded-none"
      data-a2ui={cad ? "CadViewer" : "ModelViewer"}
    >
      <div className="relative aspect-[4/3] min-h-64 max-h-[min(64vh,560px)] bg-[radial-gradient(circle_at_50%_42%,rgba(74,72,67,.72),rgba(31,30,29,.98)_72%)] fullscreen:min-h-0 fullscreen:max-h-none fullscreen:flex-1 fullscreen:aspect-auto">
        {viewerState === "ready" &&
          createElement("model-viewer", {
            src: inlineUrl(previewUrl),
            alt: name,
            "camera-controls": true,
            "touch-action": "pan-y",
            "shadow-intensity": "1",
            "environment-image": "neutral",
            "interaction-prompt": "auto",
            className: "h-full w-full",
          })}
        {viewerState !== "ready" && (
          <div className="absolute inset-0 flex items-center justify-center gap-2 text-sm text-muted-foreground">
            {viewerState === "loading" && <Loader2 className="size-4 animate-spin" />}
            <span>{viewerState === "loading" ? "Загружаю 3D-модель…" : "Не удалось открыть 3D-превью"}</span>
          </div>
        )}
        <button
          type="button"
          onClick={() => void root.current?.requestFullscreen()}
          className="absolute right-3 top-3 flex size-9 items-center justify-center rounded-full border border-white/10 bg-black/45 text-white shadow-lg backdrop-blur-md transition-colors hover:bg-black/65"
          aria-label="Открыть на весь экран"
        >
          <Maximize2 className="size-4" />
        </button>
      </div>
      <div className="flex items-center gap-3 border-t border-border px-4 py-3.5">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
          <Box className="size-5" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-xs text-muted-foreground">{cad ? "CAD-модель" : "3D-модель"}</span>
          <span className="block truncate text-sm font-medium text-foreground">{name}</span>
        </span>
        <a
          href={downloadUrl}
          download={name}
          className="flex size-9 shrink-0 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors hover:border-primary/40 hover:bg-primary hover:text-primary-foreground"
          aria-label={`Скачать ${name}`}
        >
          <Download className="size-4" />
        </a>
      </div>
    </div>
  );
}

const ModelViewer = createComponentImplementation(ModelViewerApi, ({ props }) => (
  <ArtifactViewer
    name={typeof props.name === "string" ? props.name : "3D-модель"}
    previewUrl={typeof props.url === "string" ? props.url : ""}
    downloadUrl={typeof props.url === "string" ? props.url : ""}
    cad={false}
  />
));

const CadViewer = createComponentImplementation(CadViewerApi, ({ props }) => (
  <ArtifactViewer
    name={typeof props.name === "string" ? props.name : "CAD-модель"}
    previewUrl={typeof props.previewUrl === "string" ? props.previewUrl : ""}
    downloadUrl={typeof props.sourceUrl === "string" ? props.sourceUrl : ""}
    cad
  />
));

// cardsCatalog подключается к MessageProcessor (см. useAcpRuntime).
export const cardsCatalog: Catalog<ReactComponentImplementation> = new Catalog(
  CARDS_CATALOG_ID,
  [DiffView, DownloadView, ModelViewer, CadViewer],
  [],
);
