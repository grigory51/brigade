// @vitest-environment happy-dom
import { MessageProcessor } from "@a2ui/web_core/v0_9";
import {
  AssistantRuntimeProvider,
  type ThreadMessageLike,
  useExternalStoreRuntime,
} from "@assistant-ui/react";
import { fromAgUiMessages } from "@assistant-ui/react-ag-ui";
import { cleanup, fireEvent, render, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { AcpThread } from "./AcpThread";
import { assembleHistory, type A2uiState } from "./useAcpRuntime";

afterEach(cleanup);

const a2ui: A2uiState = { processor: new MessageProcessor([]), version: 0 };
const toolNames = ["web search", "python", "Find", "Open page"];
const identity = (message: ThreadMessageLike) => message;

function Feed({ messages }: { messages: ThreadMessageLike[] }) {
  const runtime = useExternalStoreRuntime({ messages, convertMessage: identity, onNew: async () => {} });
  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <AcpThread readonly commands={[]} plan={[]} a2ui={a2ui} configOptions={[]} onConfigChange={() => {}} />
    </AssistantRuntimeProvider>
  );
}

test("keeps a mixed activity group expandable as more tool calls stream in", async () => {
  const streaming = (count: number): ThreadMessageLike[] => [{
    id: "turn",
    role: "assistant",
    status: { type: "running" },
    content: toolNames.slice(0, count).map((toolName, i) => ({
      type: "tool-call",
      toolCallId: String(i),
      toolName,
      args: { query: toolName },
      result: i < count - 1 ? "ok" : undefined,
    })),
  }];
  const view = render(<Feed messages={streaming(1)} />);
  const group = (await view.findByText("Активность · 1")).closest("details")!;
  expect(group.open).toBe(false);
  view.rerender(<Feed messages={streaming(2)} />);
  const label = await view.findByText("Активность · 2");
  expect(label.closest("details")).toBe(group);
  expect(group.open).toBe(false);
  fireEvent.click(label);
  expect(group.open).toBe(true);

  view.rerender(<Feed messages={streaming(4)} />);
  const updated = await view.findByText("Активность · 4");
  expect(updated.closest("details")).toBe(group);
  expect(group.open).toBe(true);
  expect(within(group).getAllByText("Аргументы")).toHaveLength(4);
  fireEvent.click(updated);
  expect(group.open).toBe(false);
});

test("history groups mixed calls but leaves an interactive card and the answer outside", async () => {
  const history = [...toolNames, "mcp__brigade__show_choice", "Find"].map((toolName, i) => ({
    id: String(i),
    role: "tool_call",
    content: "",
    toolName,
    argsText: JSON.stringify(toolName.endsWith("show_choice")
      ? { options: ["Продолжить"] } : { query: "example" }),
  }));
  const messages = fromAgUiMessages(assembleHistory([
    ...history, { id: "answer", role: "assistant", content: "Ответ агента" },
  ]));
  const view = render(<Feed messages={messages} />);
  const group = (await view.findByText("Активность · 4")).closest("details")!;
  expect(group.open).toBe(false);
  expect(view.getByRole("button", { name: "Продолжить" }).closest("details")).toBeNull();
  const single = (await view.findByText("Активность · 1")).closest("details")!;
  expect(single.open).toBe(false);
  expect(view.getAllByText("Find").map((name) => name.closest("details"))).toEqual([group, single]);
  expect((await view.findByText("Ответ агента")).closest("details")).toBeNull();
});

test("combines tools and reasoning into one collapsed activity in source order", async () => {
  const messages: ThreadMessageLike[] = [{
    id: "turn",
    role: "assistant",
    content: [
      ...Array.from({ length: 5 }, (_, i) => ({
        type: "tool-call" as const,
        toolCallId: String(i),
        toolName: `Tool ${i + 1}`,
        args: {},
        result: "ok",
      })),
      { type: "reasoning", text: "Проверка результата" },
      { type: "reasoning", text: "Сопоставляю найденные данные." },
      { type: "tool-call", toolCallId: "last", toolName: "Tool 6", args: {}, result: "ok" },
      { type: "text", text: "Итоговый ответ" },
    ],
  }];
  const view = render(<Feed messages={messages} />);
  const label = await view.findByText("Активность · 6");
  const group = label.closest("details")!;
  expect(group.open).toBe(false);
  expect(view.container.querySelectorAll(".acp-tool-activity")).toHaveLength(1);
  expect((await view.findByText("Итоговый ответ")).closest("details")).toBeNull();

  fireEvent.click(label);
  expect(group.open).toBe(true);
  expect(group.querySelectorAll("details, [data-slot='reasoning-trigger']")).toHaveLength(0);
  expect(within(group).getByText("Рассуждения")).toBeTruthy();
  const items = within(group).getAllByText(/^(Tool \d|Проверка результата|Сопоставляю найденные данные\.)$/);
  expect(items.map((item) => item.textContent)).toEqual([
    "Tool 1", "Tool 2", "Tool 3", "Tool 4", "Tool 5",
    "Проверка результата", "Сопоставляю найденные данные.", "Tool 6",
  ]);
});

test("reasoning-only activity stays collapsed while streaming and when complete", async () => {
  const messages = (running: boolean): ThreadMessageLike[] => [{
    id: "turn",
    role: "assistant",
    status: running ? { type: "running" } : { type: "complete", reason: "stop" },
    content: [{ type: "reasoning", text: "Проверяю гипотезу." }],
  }];
  const view = render(<Feed messages={messages(true)} />);
  const group = (await view.findByText("Проверяю гипотезу.")).closest("details")!;
  expect(group.open).toBe(false);
  expect(group.querySelector("summary")?.textContent).toBe("Рассуждения");
  expect(within(group).getByLabelText("Выполняется")).toBeTruthy();

  view.rerender(<Feed messages={messages(false)} />);
  expect((await view.findByText("Проверяю гипотезу.")).closest("details")).toBe(group);
  expect(group.open).toBe(false);
  expect(within(group).queryByLabelText("Выполняется")).toBeNull();
});

test.each([false, true])("preserves activity open=%s when reasoning grows into tool calls", async (open) => {
  const messages = (withTool: boolean): ThreadMessageLike[] => [{
    id: "turn",
    role: "assistant",
    status: { type: "running" },
    content: [
      { type: "reasoning", text: "Проверяю данные." },
      ...(withTool ? [{ type: "tool-call" as const, toolCallId: "tool", toolName: "Find", args: {} }] : []),
    ],
  }];
  const view = render(<Feed messages={messages(false)} />);
  const group = (await view.findByText("Проверяю данные.")).closest("details")!;
  if (open) fireEvent.click(group.querySelector("summary")!);
  expect(group.open).toBe(open);
  view.rerender(<Feed messages={messages(true)} />);
  expect((await view.findByText("Активность · 1")).closest("details")).toBe(group);
  expect(group.open).toBe(open);
});

test("visible answers and standalone cards separate activities without inheriting running status", async () => {
  const messages: ThreadMessageLike[] = [{
    id: "turn",
    role: "assistant",
    status: { type: "running" },
    content: [
      { type: "reasoning", text: "Первое рассуждение." },
      { type: "tool-call", toolCallId: "first", toolName: "Find", args: {}, result: "ok" },
      { type: "text", text: "Промежуточный ответ" },
      { type: "reasoning", text: "Второе рассуждение." },
      { type: "tool-call", toolCallId: "choice", toolName: "mcp__brigade__show_choice", args: { options: ["Продолжить"] }, result: "ok" },
      { type: "reasoning", text: "Текущее рассуждение." },
      { type: "tool-call", toolCallId: "last", toolName: "Search", args: {} },
    ],
  }];
  const view = render(<Feed messages={messages} />);
  await view.findByText("Текущее рассуждение.");
  const groups = Array.from(view.container.querySelectorAll<HTMLDetailsElement>(".acp-tool-activity"));
  expect(groups).toHaveLength(3);
  expect(groups.every((group) => !group.open)).toBe(true);
  expect(groups.map((group) => within(group).queryByLabelText("Выполняется") !== null)).toEqual([false, false, true]);
  const answer = view.getByText("Промежуточный ответ");
  const choice = view.getByRole("button", { name: "Продолжить" });
  expect(answer.closest("details")).toBeNull();
  expect(choice.closest("details")).toBeNull();
  for (const [before, after] of [[groups[0], answer], [answer, groups[1]], [groups[1], choice], [choice, groups[2]]]) {
    expect(before.compareDocumentPosition(after) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  }
});
