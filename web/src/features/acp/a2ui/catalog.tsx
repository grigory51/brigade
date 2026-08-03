import { z } from "zod";
import { Catalog, DynamicValueSchema } from "@a2ui/web_core/v0_9";
import {
  basicCatalog,
  createComponentImplementation,
  type ReactComponentImplementation,
} from "@a2ui/react/v0_9";
import { Download, FileArchive } from "lucide-react";
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

// cardsCatalog подключается к MessageProcessor (см. useAcpRuntime).
export const cardsCatalog: Catalog<ReactComponentImplementation> = new Catalog(
  CARDS_CATALOG_ID,
  [DiffView, DownloadView],
  [],
);
