import { Component, useContext, useEffect, useMemo, useState } from "react";
import { Loader2, LayoutTemplate, AlertTriangle } from "lucide-react";
import type { ToolCallMessagePartComponent } from "@assistant-ui/react";
import { A2uiSurface } from "@a2ui/react/v0_9";
import type { A2uiMessage, MessageProcessor } from "@a2ui/web_core/v0_9";
import type { ReactComponentImplementation } from "@a2ui/react/v0_9";
import { A2uiContext } from "./context";
import { BASIC_CATALOG_ID } from "./catalog";

type A2uiProcessor = MessageProcessor<ReactComponentImplementation>;

// RenderUiSpec — валидированные аргументы вызова render_ui: плоский список компонентов
// A2UI (с обязательным root) и опциональная модель данных.
type RenderUiSpec = {
  components: Array<Record<string, unknown>>;
  dataModel?: Record<string, unknown>;
};

// parseRenderUiArgs разбирает сырой argsText вызова. Возвращает null, если JSON неполон/
// невалиден (аргументы стримятся — до закрытия JSON парс почти всегда падает) ИЛИ если в
// списке нет компонента с id:"root" (A2uiSurface рендерит именно root — без него
// поверхность пуста). Это гейт против отрисовки полу-собранного дерева. Осознанно НЕ
// опираемся на props.args (частичный парс агрегатора): неполный JSON дал бы обрезанный
// список компонентов и сломанный промежуточный кадр.
export function parseRenderUiArgs(
  argsText: string | undefined,
): RenderUiSpec | null {
  if (!argsText) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(argsText);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object") return null;
  const components = (parsed as { components?: unknown }).components;
  if (!Array.isArray(components)) return null;
  const hasRoot = components.some(
    (c) => c && typeof c === "object" && (c as { id?: unknown }).id === "root",
  );
  if (!hasRoot) return null;
  const dataModel = (parsed as { dataModel?: unknown }).dataModel;
  return {
    components: components as Array<Record<string, unknown>>,
    dataModel:
      dataModel && typeof dataModel === "object"
        ? (dataModel as Record<string, unknown>)
        : undefined,
  };
}

// buildOrUpdateSurface скармливает процессору A2UI-сообщения для поверхности surfaceId.
// Первый раз — createSurface(basicCatalog) + updateComponents + (опц.) updateDataModel;
// повторно — только updateComponents/updateDataModel (upsert платформы: заменяет пропсы,
// не пересоздаёт, не мигает). createSurface под guard'ом getSurface: платформа бросает
// A2uiStateError на дубль surfaceId. Форма сообщений зеркалит бэкенд (a2ui.DiffSurface).
function buildOrUpdateSurface(
  processor: A2uiProcessor,
  surfaceId: string,
  spec: RenderUiSpec,
): void {
  const messages: A2uiMessage[] = [];
  if (!processor.model.getSurface(surfaceId)) {
    messages.push({
      version: "v0.9",
      createSurface: { surfaceId, catalogId: BASIC_CATALOG_ID },
    } as A2uiMessage);
  }
  messages.push({
    version: "v0.9",
    updateComponents: { surfaceId, components: spec.components },
  } as A2uiMessage);
  if (spec.dataModel) {
    messages.push({
      version: "v0.9",
      updateDataModel: { surfaceId, path: "/", value: spec.dataModel },
    } as A2uiMessage);
  }
  processor.processMessages(messages);
}

// RenderUiCard рендерит вызов render_ui как A2UI-поверхность. Поверхность строится в
// effect'е (surfaceId = toolCallId), после чего onSurfaceCreated бампит версию контекста
// → карточка ре-рендерится и находит поверхность в surfacesMap. До готовности — скелетон.
// Тот же путь работает и при восстановлении из истории: карточка приходит role="tool_call"
// с argsText, parseRenderUiArgs строит поверхность заново.
export const RenderUiCard: ToolCallMessagePartComponent = (props) => {
  const a2ui = useContext(A2uiContext);
  const processor = a2ui?.processor;
  const surfaceId = props.toolCallId;
  const spec = useMemo(
    () => parseRenderUiArgs(props.argsText),
    [props.argsText],
  );
  const [buildError, setBuildError] = useState(false);

  useEffect(() => {
    if (!processor || !spec) return;
    try {
      buildOrUpdateSurface(processor, surfaceId, spec);
      setBuildError(false);
    } catch {
      // Невалидный каталог/структура — платформа бросает A2uiStateError.
      setBuildError(true);
    }
  }, [processor, spec, surfaceId]);

  const surface = processor?.model.surfacesMap.get(surfaceId);
  if (surface && !buildError) {
    return (
      // Error boundary: невалидные пропсы компонента могут бросить в GenericBinder при
      // рендере — ловим, чтобы не уронить всю ленту (key=surfaceId стабилен на карточку).
      <RenderUiBoundary key={surfaceId}>
        <A2uiSurface surface={surface} />
      </RenderUiBoundary>
    );
  }

  const complete = props.status.type === "complete";
  return (
    <RenderUiSkeleton
      failed={buildError || (complete && !spec)}
      argsText={props.argsText}
    />
  );
};

// RenderUiSkeleton — состояние до готовности поверхности: спиннер, пока UI строится
// (стрим не завершён), либо компактная карточка ошибки, если стрим кончился, а разобрать
// или отрисовать не удалось (с раскрытием сырого argsText для отладки).
function RenderUiSkeleton({
  failed,
  argsText,
}: {
  failed: boolean;
  argsText?: string;
}) {
  if (failed) {
    return (
      <div className="space-y-2 rounded-lg border border-dashed border-border bg-card/40 p-3 text-sm">
        <div className="flex items-center gap-2 text-muted-foreground">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span>Не удалось построить UI</span>
        </div>
        {argsText && (
          <details className="text-xs">
            <summary className="cursor-pointer text-muted-foreground">
              Аргументы
            </summary>
            <pre className="mt-1 max-h-48 overflow-auto rounded bg-muted/50 p-2 font-mono">
              {argsText}
            </pre>
          </details>
        )}
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed border-border bg-card/40 p-3 text-sm text-muted-foreground">
      <LayoutTemplate className="size-3.5 shrink-0" />
      <Loader2 className="size-3 shrink-0 animate-spin" />
      <span>строю интерфейс…</span>
    </div>
  );
}

// RenderUiBoundary изолирует падение рендера A2UI-поверхности (невалидные пропсы →
// исключение в GenericBinder) от остальной ленты.
class RenderUiBoundary extends Component<
  { children: React.ReactNode },
  { hasError: boolean }
> {
  state = { hasError: false };

  static getDerivedStateFromError() {
    return { hasError: true };
  }

  componentDidCatch() {
    // Проглатываем: ошибка одной агентной карточки не должна ронять чат.
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="flex items-center gap-2 rounded-lg border border-dashed border-border bg-card/40 p-3 text-sm text-muted-foreground">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span>Не удалось отрисовать UI (невалидные свойства компонента).</span>
        </div>
      );
    }
    return this.props.children;
  }
}
