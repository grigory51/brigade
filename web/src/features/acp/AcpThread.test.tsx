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
