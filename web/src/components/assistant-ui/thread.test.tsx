// @vitest-environment happy-dom
import {
  AssistantRuntimeProvider,
  useExternalStoreRuntime,
  type AppendMessage,
  type ThreadMessageLike,
} from "@assistant-ui/react";
import { cleanup, fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PendingContextProvider, usePendingContext } from "./composer-context";
import { Thread, type ThreadComponents, type ThreadProps } from "./thread";

afterEach(cleanup);

const identity = (message: ThreadMessageLike) => message;
const emptyMessages: ThreadMessageLike[] = [];
const noop = async () => {};
const components: ThreadComponents = { ToolFallback: () => null, ToolGroup: ({ children }) => <div data-testid="activity">{children}</div> };

function ContextButton() {
  const context = usePendingContext();
  return <button onClick={() => context?.add({ kind: "file", path: "uploads/spec.txt", name: "spec.txt" })}>Добавить контекст</button>;
}

function Feed({
  messages = emptyMessages,
  isRunning = false,
  isLoading = false,
  onNew = noop,
  onCancel = noop,
  editable = false,
  reloadable = false,
  context = false,
  ...props
}: Omit<ThreadProps, "components"> & {
  messages?: ThreadMessageLike[];
  isRunning?: boolean;
  isLoading?: boolean;
  onNew?: (message: AppendMessage) => Promise<void>;
  onCancel?: () => Promise<void>;
  editable?: boolean;
  reloadable?: boolean;
  context?: boolean;
}) {
  const runtime = useExternalStoreRuntime({
    messages, convertMessage: identity, isRunning, isLoading, onNew, onCancel,
    onEdit: editable ? noop : undefined,
    onReload: reloadable ? noop : undefined,
  });
  return (
    <TooltipProvider>
      <AssistantRuntimeProvider runtime={runtime}>
        <PendingContextProvider>
          {context && <ContextButton />}
          <Thread components={components} {...props} />
        </PendingContextProvider>
      </AssistantRuntimeProvider>
    </TooltipProvider>
  );
}

test("shows model and permissions by the input and groups response style and reasoning in settings", async () => {
  const view = render(<Feed configOptions={[
    { id: "model", name: "Model", category: "model", currentValue: "model-a", options: [{ value: "model-a", name: "Model A" }] },
    { id: "effort", name: "Effort", category: "thought_level", currentValue: "medium", options: [{ value: "medium", name: "Medium" }] },
    { id: "mode", name: "Mode", category: "mode", currentValue: "auto", options: [{ value: "auto", name: "Auto" }] },
  ]} responseProfiles={[{ id: "default", name: "Обычно" }]} />);
  expect(view.getByRole("button", { name: "Модель: Model A" }).textContent).toBe("Model A");
  expect(view.getByRole("button", { name: "Права агента: Авто" })).toBeTruthy();
  expect(view.queryByText("Глубина рассуждений")).toBeNull();
  expect(view.queryByText("Стиль ответа")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Настройки ответа" }));
  const settings = await view.findByRole("dialog", { name: "Настройки ответа" });
  expect(within(settings).getByText("Стиль ответа")).toBeTruthy();
  expect(within(settings).getByText("Глубина рассуждений")).toBeTruthy();
  expect(within(settings).getByRole("button", { name: "Глубина рассуждений: Средняя" })).toBeTruthy();
  expect(within(settings).getByRole("button", { name: "Стиль ответа: Обычно" })).toBeTruthy();
});

test("empty and whitespace-only input disables Send; suggestions only fill the draft", async () => {
  const onNew = vi.fn(noop);
  const view = render(<Feed onNew={onNew} />);
  const send = view.getByRole("button", { name: "Отправить сообщение" }) as HTMLButtonElement;
  const input = view.getByRole("textbox", { name: "Сообщение агенту" }) as HTMLTextAreaElement;
  expect(send.disabled).toBe(true);
  fireEvent.change(input, { target: { value: "   " } });
  expect(send.disabled).toBe(true);
  fireEvent.click(view.getByRole("button", { name: "Объясни, как устроен проект" }));
  expect(input.value).toBe("Объясни, как устроен проект");
  expect(send.disabled).toBe(false);
  expect(onNew).not.toHaveBeenCalled();
  fireEvent.click(send);
  await waitFor(() => expect(onNew).toHaveBeenCalledOnce());
});

test("permissions remain available during generation while the model is locked", () => {
  const view = render(<Feed isRunning configOptions={[
    { id: "model", name: "Model", category: "model", currentValue: "model-a", options: [{ value: "model-a", name: "Model A" }] },
    { id: "mode", name: "Mode", category: "mode", currentValue: "auto", options: [{ value: "auto", name: "Approve for me" }] },
  ]} />);
  expect((view.getByRole("button", { name: "Модель: Model A" }) as HTMLButtonElement).disabled).toBe(true);
  expect((view.getByRole("button", { name: "Права агента: Автоодобрение" }) as HTMLButtonElement).disabled).toBe(false);
});

test("context attachments enable Send even with an empty draft and retain their payload", async () => {
  const onNew = vi.fn<(message: AppendMessage) => Promise<void>>(noop);
  const view = render(<Feed onNew={onNew} context />);
  fireEvent.click(view.getByRole("button", { name: "Добавить контекст" }));
  const send = view.getByRole("button", { name: "Отправить сообщение" }) as HTMLButtonElement;
  expect(send.disabled).toBe(false);
  fireEvent.click(send);
  await waitFor(() => expect(onNew).toHaveBeenCalledOnce());
  expect(onNew.mock.calls[0]?.[0]).toMatchObject({ content: [{ type: "text", text: "Контекст:\nПриложенные файлы: uploads/spec.txt" }] });
  expect(view.queryByText("spec.txt")).toBeNull();
});

test("loading history disables context-only Send", () => {
  const view = render(<Feed isLoading context />);
  fireEvent.click(view.getByRole("button", { name: "Добавить контекст" }));
  expect((view.getByRole("button", { name: "Отправить сообщение" }) as HTMLButtonElement).disabled).toBe(true);
});

test("running keeps Stop instead of Send even with context attachments", async () => {
  const onCancel = vi.fn(noop);
  const view = render(<Feed isRunning onCancel={onCancel} context />);
  fireEvent.click(view.getByRole("button", { name: "Добавить контекст" }));
  expect(view.queryByRole("button", { name: "Отправить сообщение" })).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Остановить ответ" }));
  await waitFor(() => expect(onCancel).toHaveBeenCalledOnce());
});

const conversation: ThreadMessageLike[] = [
  { id: "user", role: "user", content: "Вопрос" },
  { id: "assistant", role: "assistant", content: "Ответ", status: { type: "complete", reason: "stop" } },
];

test("shows direct copy/retry actions and capability-gated editing, with no synthetic timestamps", () => {
  const view = render(<Feed messages={conversation} reloadable />);
  expect(view.getByRole("button", { name: "Копировать ответ" })).toBeTruthy();
  expect(view.getByRole("button", { name: "Повторить ответ" })).toBeTruthy();
  expect(view.getByRole("button", { name: "Копировать сообщение" })).toBeTruthy();
  expect(view.queryByRole("button", { name: "Редактировать ответ" })).toBeNull();
  expect(view.queryByRole("button", { name: "Редактировать сообщение" })).toBeNull();
  expect(view.container.querySelector("time")).toBeNull();
  view.rerender(<Feed messages={conversation} reloadable editable />);
  expect(view.getByRole("button", { name: "Редактировать ответ" })).toBeTruthy();
  expect(view.getByRole("button", { name: "Редактировать сообщение" })).toBeTruthy();
  const actions = view.container.querySelector(".aui-user-action-bar-root")!;
  expect(actions.className).toContain("group-focus-within/user:opacity-100");
  fireEvent.click(view.getByRole("button", { name: "Редактировать сообщение" }));
  expect(view.getByRole("textbox", { name: "Редактирование сообщения" })).toBeTruthy();
  expect(view.getByRole("button", { name: "Отмена" })).toBeTruthy();
});

test("readonly empty thread has no prompts or composer and retains the 45rem width", () => {
  const view = render(<Feed readonly />);
  expect(view.queryByRole("button", { name: "Объясни, как устроен проект" })).toBeNull();
  expect(view.queryByRole("textbox")).toBeNull();
  expect((view.container.querySelector(".aui-thread-root") as HTMLElement).style.getPropertyValue("--thread-max-width")).toBe("45rem");
});

test("timestamps use only valid ISO dates from transport metadata, never runtime createdAt", () => {
  const createdAt = "2025-06-12T14:25:00.000Z";
  const messages: ThreadMessageLike[] = conversation.map((message) => ({
    ...message,
    createdAt: new Date("2026-11-25T00:00:00.000Z"),
    metadata: { custom: { createdAt } },
  }));
  const view = render(<Feed messages={messages} />);
  const timestamps = view.container.querySelectorAll("time");
  expect(timestamps).toHaveLength(2);
  for (const timestamp of timestamps) expect(timestamp.dateTime).toBe(createdAt);
  view.rerender(<Feed messages={messages.map((message) => ({ ...message, metadata: { custom: { createdAt: "invalid" } } }))} />);
  expect(view.container.querySelector("time")).toBeNull();
  view.rerender(<Feed messages={messages.map((message) => ({ ...message, metadata: { custom: {} } }))} />);
  expect(view.container.querySelector("time")).toBeNull();
});
