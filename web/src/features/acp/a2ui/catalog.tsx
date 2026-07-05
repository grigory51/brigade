import { z } from "zod";
import { Catalog, DynamicValueSchema } from "@a2ui/web_core/v0_9";
import {
  basicCatalog,
  createComponentImplementation,
  type ReactComponentImplementation,
} from "@a2ui/react/v0_9";
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

// cardsCatalog подключается к MessageProcessor (см. useAcpRuntime).
export const cardsCatalog: Catalog<ReactComponentImplementation> = new Catalog(
  CARDS_CATALOG_ID,
  [DiffView],
  [],
);
