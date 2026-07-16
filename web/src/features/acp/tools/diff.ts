// Разбор diff-результатов tool call'ов и построчное сравнение для DiffCard.
//
// Бэкенд транслирует ACP ToolCallContent с diff-блоками в TOOL_CALL_RESULT как JSON
// вида [{"type":"diff","path":...,"oldText":...,"newText":...}] («липкий diff» — см.
// backend/internal/acp/translate.go). Клиентский агрегатор уже парсит JSON, поэтому
// сюда результат приходит массивом объектов.

export type DiffBlock = {
  path: string;
  oldText: string;
  newText: string;
};

// parseDiffResult извлекает diff-блоки из результата tool call. null — результат не
// является diff-контентом (рендерится другой карточкой/фоллбеком).
export function parseDiffResult(result: unknown): DiffBlock[] | null {
  if (!Array.isArray(result)) return null;
  const blocks: DiffBlock[] = [];
  for (const item of result) {
    const o = item as Record<string, unknown>;
    if (o?.type !== "diff" || typeof o.newText !== "string") return null;
    blocks.push({
      path: typeof o.path === "string" ? o.path : "",
      oldText: typeof o.oldText === "string" ? o.oldText : "",
      newText: o.newText,
    });
  }
  return blocks.length > 0 ? blocks : null;
}

type DiffLine = {
  kind: "ctx" | "del" | "add";
  text: string;
  oldNo?: number;
  newNo?: number;
};

// contextLines — сколько неизменённых строк показывать вокруг изменённого блока.
const contextLines = 2;

// lineDiff строит построчное сравнение по общему префиксу/суффиксу: середина —
// удалённые и добавленные строки, вокруг — усечённый контекст. Для типичной правки
// агента (замена одного фрагмента) этого достаточно; полноценный LCS здесь избыточен.
export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = oldText.split("\n");
  const b = newText.split("\n");

  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) {
    prefix++;
  }
  let suffix = 0;
  while (
    suffix < a.length - prefix &&
    suffix < b.length - prefix &&
    a[a.length - 1 - suffix] === b[b.length - 1 - suffix]
  ) {
    suffix++;
  }

  const lines: DiffLine[] = [];
  for (let i = Math.max(0, prefix - contextLines); i < prefix; i++) {
    lines.push({ kind: "ctx", text: a[i], oldNo: i + 1, newNo: i + 1 });
  }
  for (let i = prefix; i < a.length - suffix; i++) {
    lines.push({ kind: "del", text: a[i], oldNo: i + 1 });
  }
  for (let i = prefix; i < b.length - suffix; i++) {
    lines.push({ kind: "add", text: b[i], newNo: i + 1 });
  }
  for (let i = 0; i < Math.min(contextLines, suffix); i++) {
    const ai = a.length - suffix + i;
    const bi = b.length - suffix + i;
    lines.push({ kind: "ctx", text: a[ai], oldNo: ai + 1, newNo: bi + 1 });
  }
  return lines;
}
