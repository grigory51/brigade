// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { toast } from "sonner";
import { telegramClient } from "@/api/client";
import { TelegramBot } from "@/api/gen/brigade/v1/telegram_pb";
import { TelegramSection } from "./TelegramSection";

vi.mock("@/api/client", () => ({
  agentClient: {
    listConnections: () => Promise.resolve({
      connections: [{ id: "connection-1", name: "Claude", agentType: "claude", authProfile: "default" }],
    }),
  },
  mcpClient: { listServers: () => Promise.resolve({ servers: [] }) },
  authClient: { getAgentImages: () => Promise.resolve({ images: [] }) },
  telegramClient: { saveBot: vi.fn(), createBindingLink: vi.fn() },
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(telegramClient.saveBot).mockResolvedValue(new TelegramBot({ id: "saved", ownerConnected: true }));
});

afterEach(cleanup);

async function mountSection(bots: TelegramBot[] = [], selectedId = "new") {
  const props = { bots, selectedId, mode: "poll", onSelect: vi.fn(), onChange: vi.fn() };
  const view = render(<TelegramSection {...props} />);
  await act(async () => {});
  return { ...view, props };
}

async function choose(label: string, option: string) {
  fireEvent.keyDown(screen.getByRole("combobox", { name: label }), { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name: option }));
}

test("new bots default to threads and save archive", async () => {
  const view = await mountSection();
  expect(view.getByRole("combobox", { name: "Режим сессий" }).textContent).toContain("Треды");
  expect(view.queryByRole("combobox", { name: "При /new" })).toBeNull();
  expect(view.getByText(/следующее сообщение создаёт новую/)).toBeTruthy();
  expect(view.getByText(/Для архивации нужен репозиторий заметок/)).toBeTruthy();
  fireEvent.change(view.getByPlaceholderText("123456:ABC…"), { target: { value: "test-token" } });
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Сохранить" })); });
  expect(telegramClient.saveBot).toHaveBeenCalledExactlyOnceWith({
    bot: {
      id: "", agentType: "claude", authProfile: "connection-1", image: "", mcpServerIds: [],
      sessionMode: "threads", newSessionAction: "archive",
    },
    token: "test-token",
  });
});

test("chat reveals the action and contextual safety hints with accessible descriptions", async () => {
  const view = await mountSection();
  await choose("Режим сессий", "Обычный");
  const mode = view.getByRole("combobox", { name: "Режим сессий" });
  const action = view.getByRole("combobox", { name: "При /new" });
  expect(action.textContent).toContain("Архивировать");
  expect(document.getElementById(mode.getAttribute("aria-describedby")!)?.textContent)
    .toContain("Одна сессия на чат без разделения по топикам");
  expect(document.getElementById(action.getAttribute("aria-describedby")!)?.textContent)
    .toContain("При ошибке история не удаляется");
  expect(view.getByText(/Смена режима не удаляет старые сессии/)).toBeTruthy();
  await choose("При /new", "Удалять");
  expect(document.getElementById(action.getAttribute("aria-describedby")!)?.textContent)
    .toContain("История Brigade удаляется необратимо. Сообщения в Telegram остаются.");
  expect(view.queryByText(/Для архивации нужен репозиторий заметок/)).toBeNull();
  await choose("Режим сессий", "Треды");
  expect(view.queryByRole("combobox", { name: "При /new" })).toBeNull();
  expect(view.queryByText(/История Brigade удаляется необратимо/)).toBeNull();
});

test.each([
  ["Архивировать", "archive"],
  ["Удалять", "delete"],
])("new chat bots save %s", async (label, newSessionAction) => {
  const view = await mountSection();
  await choose("Режим сессий", "Обычный");
  await choose("При /new", label);
  fireEvent.change(view.getByPlaceholderText("123456:ABC…"), { target: { value: "test-token" } });
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Сохранить" })); });
  expect(telegramClient.saveBot).toHaveBeenCalledExactlyOnceWith({
    bot: {
      id: "", agentType: "claude", authProfile: "connection-1", image: "", mcpServerIds: [],
      sessionMode: "chat", newSessionAction,
    },
    token: "test-token",
  });
});

test("editing loads both fields and saves changes without replacing other settings", async () => {
  const bot = new TelegramBot({
    id: "existing", username: "helper", tokenSet: true, ownerConnected: true,
    agentType: "claude", authProfile: "connection-1", image: "custom:v1", mcpServerIds: ["mcp-1"],
    sessionMode: "chat", newSessionAction: "delete",
  });
  const view = await mountSection([bot], bot.id);
  expect(view.getByRole("combobox", { name: "Режим сессий" }).textContent).toContain("Обычный");
  expect(view.getByRole("combobox", { name: "При /new" }).textContent).toContain("Удалять");
  await choose("При /new", "Архивировать");
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Сохранить" })); });
  expect(telegramClient.saveBot).toHaveBeenCalledExactlyOnceWith({
    bot: {
      id: "existing", agentType: "claude", authProfile: "connection-1", image: "custom:v1", mcpServerIds: ["mcp-1"],
      sessionMode: "chat", newSessionAction: "archive",
    },
    token: "",
  });
});

test("selecting legacy and new bots resets fields to threads/archive", async () => {
  const bots = [
    new TelegramBot({ id: "chat", sessionMode: "chat", newSessionAction: "delete" }),
    new TelegramBot({ id: "legacy" }),
  ];
  const view = await mountSection(bots, "chat");
  view.rerender(<TelegramSection {...view.props} selectedId="legacy" />);
  expect(view.getByRole("combobox", { name: "Режим сессий" }).textContent).toContain("Треды");
  expect(view.queryByRole("combobox", { name: "При /new" })).toBeNull();
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Сохранить" })); });
  expect(telegramClient.saveBot).toHaveBeenCalledWith(expect.objectContaining({
    bot: expect.objectContaining({ id: "legacy", sessionMode: "threads", newSessionAction: "archive" }),
  }));
  await choose("Режим сессий", "Обычный");
  expect(view.getByRole("combobox", { name: "При /new" }).textContent).toContain("Архивировать");
  view.rerender(<TelegramSection {...view.props} selectedId="chat" />);
  expect(view.getByRole("combobox", { name: "При /new" }).textContent).toContain("Удалять");
  view.rerender(<TelegramSection {...view.props} selectedId="new" />);
  expect(view.getByRole("combobox", { name: "Режим сессий" }).textContent).toContain("Треды");
  await choose("Режим сессий", "Обычный");
  expect(view.getByRole("combobox", { name: "При /new" }).textContent).toContain("Архивировать");
});

test("switching an existing delete bot to threads saves archive", async () => {
  const view = await mountSection([new TelegramBot({ id: "bot", sessionMode: "chat", newSessionAction: "delete" })], "bot");
  await choose("Режим сессий", "Треды");
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Сохранить" })); });
  expect(telegramClient.saveBot).toHaveBeenCalledWith(expect.objectContaining({
    bot: expect.objectContaining({ id: "bot", sessionMode: "threads", newSessionAction: "archive" }),
  }));
});

test("saving disables both selects and failure preserves the draft for retry", async () => {
  let rejectSave!: (error: Error) => void;
  vi.mocked(telegramClient.saveBot).mockImplementationOnce(() => new Promise((_, reject) => { rejectSave = reject; }));
  const view = await mountSection([new TelegramBot({ id: "bot", sessionMode: "chat" })], "bot");
  expect(view.getByRole("combobox", { name: "При /new" }).textContent).toContain("Архивировать");
  await choose("При /new", "Удалять");
  fireEvent.click(view.getByRole("button", { name: "Сохранить" }));
  const mode = view.getByRole("combobox", { name: "Режим сессий" }) as HTMLButtonElement;
  const action = view.getByRole("combobox", { name: "При /new" }) as HTMLButtonElement;
  expect(mode.disabled).toBe(true);
  expect(action.disabled).toBe(true);
  await act(async () => { rejectSave(new Error("offline")); });
  expect(mode.disabled).toBe(false);
  expect(action.disabled).toBe(false);
  expect(mode.textContent).toContain("Обычный");
  expect(action.textContent).toContain("Удалять");
  expect(toast.error).toHaveBeenCalledWith("Не удалось сохранить Telegram-бота");
  expect(view.props.onChange).not.toHaveBeenCalled();
  expect(view.props.onSelect).not.toHaveBeenCalled();
});
