// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { unstable_memoizeMarkdownComponents as memoize } from "@assistant-ui/react-markdown";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { BufferedTable, rehypeTableBuffer } from "./buffered-table";

const part = vi.hoisted(() => ({ text: "", status: { type: "running" } }));
vi.mock("@assistant-ui/react", () => ({ useMessagePartText: () => part }));

const components = memoize({ table: BufferedTable });
const header = "| Имя | Значение |\n| --- | --- |\n";
const table = header + "| Длинное имя | 123 |";

function View({ text }: { text: string }) {
  return <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeTableBuffer]} components={components}>{text}</ReactMarkdown>;
}

beforeEach(() => { part.text = ""; part.status = { type: "running" }; });
afterEach(cleanup);

test("streaming rows keep one loader and do not render table cells", () => {
  part.text = header;
  const view = render(<View text={part.text} />);
  const loader = view.getByRole("status");
  for (let length = header.length + 1; length <= table.length; length++) {
    part.text = table.slice(0, length);
    view.rerender(<View text={part.text} />);
    expect(view.getByRole("status")).toBe(loader);
    expect(view.queryByRole("table")).toBeNull();
  }
});

test.each(["\n\n", "\n\nСледующий абзац", "\n# Следующий раздел"])("flushes a closed table while the answer continues: %j", (suffix) => {
  part.text = table + suffix;
  const view = render(<View text={part.text} />);
  expect(view.getByRole("table")).toBeTruthy();
  expect(view.queryByRole("status")).toBeNull();
});

test.each(["complete", "incomplete"])("flushes the last table on %s after deferred text catches up", (status) => {
  part.text = table;
  part.status = { type: status };
  const view = render(<View text={header} />);
  expect(view.queryByRole("table")).toBeNull();
  view.rerender(<View text={table} />);
  expect(view.getByRole("cell", { name: "Длинное имя" })).toBeTruthy();
  expect(view.queryByRole("status")).toBeNull();
});

test("previous table and surrounding text stay visible while the next table streams", () => {
  part.text = `Введение\n\n${table}\n\nПродолжение\n\n${header}`;
  const view = render(<View text={part.text} />);
  expect(view.getAllByRole("table")).toHaveLength(1);
  expect(view.getAllByRole("status")).toHaveLength(1);
  expect(view.getByText("Введение")).toBeTruthy();
  expect(view.getByText("Продолжение")).toBeTruthy();
});

test("GFM tables inside blockquotes are buffered", () => {
  part.text = table.split("\n").map(line => `> ${line}`).join("\n");
  const view = render(<View text={part.text} />);
  expect(view.getByRole("status")).toBeTruthy();
  part.text += "\n>\n";
  view.rerender(<View text={part.text} />);
  expect(view.getByRole("table")).toBeTruthy();
});

test.each(["```md\n" + table + "\n```", "a \\| b\n--- | ---", "обычный текст"])("does not buffer non-table markdown: %j", (text) => {
  part.text = text;
  const view = render(<View text={text} />);
  expect(view.queryByRole("status")).toBeNull();
});
