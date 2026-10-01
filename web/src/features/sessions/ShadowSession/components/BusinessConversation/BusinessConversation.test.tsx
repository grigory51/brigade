// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { sessionClient, telegramClient } from "@/api/client";
import { BusinessConversation } from "./BusinessConversation";

vi.mock("@/api/client", () => ({
  sessionClient: { get: vi.fn().mockResolvedValue({ session: { name: "Telegram · Анна" } }), createDraft: vi.fn(), editDraft: vi.fn(), addMessage: vi.fn(), editMessage: vi.fn().mockResolvedValue({}), deleteMessage: vi.fn().mockResolvedValue({}), generateDraft: vi.fn(), cancelDraft: vi.fn(), getModels: vi.fn(), setModel: vi.fn() },
  telegramClient: { listBots: vi.fn().mockResolvedValue({ bots: [{ id: "bot", username: "helper", businessCanReply: true, sendDelaySeconds: 5 }] }), sendDraft: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers(); });

const incoming = new AcpMessage({
  id: "incoming", author: "contact", content: "Привет", source: "telegram-business/bot/conn/1",
  delivery: "received", createdAt: BigInt(Math.floor(Date.now() / 1000)),
});
const perform = async (_name: string, action: () => Promise<unknown>) => { await action(); return true; };

test("manual reply waits five seconds and cancellation never sends to Telegram", async () => {
  vi.useFakeTimers();
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  fireEvent.change(screen.getByRole("textbox", { name: "Ответ" }), { target: { value: "Добрый день" } });
  fireEvent.click(screen.getByRole("button", { name: "Отправить ответ" }));
  expect(screen.getByText(/Отправим через 5 с/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Отменить" }));
  await act(async () => { vi.advanceTimersByTime(6000); });
  expect(telegramClient.sendDraft).not.toHaveBeenCalled();
  expect((screen.getByRole("textbox", { name: "Ответ" }) as HTMLTextAreaElement).value).toBe("Добрый день");
});

test("owner can edit and delete a local note", async () => {
  const note = new AcpMessage({
    id: "note", author: "owner", content: "Старая заметка", source: "brigade",
    delivery: "received", createdAt: BigInt(Math.floor(Date.now() / 1000)),
  });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming, note]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  fireEvent.click(screen.getByRole("button", { name: "Редактировать заметку" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Текст заметки" }), { target: { value: "Новая заметка" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Сохранить" })); });
  expect(sessionClient.editMessage).toHaveBeenCalledWith({ sessionId: "session", messageId: "note", content: "Новая заметка" });
  fireEvent.click(screen.getByRole("button", { name: "Удалить заметку" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Удалить" })); });
  expect(sessionClient.deleteMessage).toHaveBeenCalledWith({ sessionId: "session", messageId: "note" });
});

test("outdated agent draft is not offered as a reply", async () => {
  const stale = new AcpMessage({
    id: "old-draft", author: "agent", content: "Старый ответ", source: "agent",
    delivery: "stale", createdAt: BigInt(Math.floor(Date.now() / 1000)),
  });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming, stale]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  expect(screen.queryByText("Старый ответ")).toBeNull();
  expect(screen.queryByText("Черновик агента")).toBeNull();
});
