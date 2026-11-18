// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { sessionClient, telegramClient } from "@/api/client";
import { BusinessConversation } from "./BusinessConversation";

vi.mock("@/api/client", () => ({
  sessionClient: { get: vi.fn().mockResolvedValue({ session: { name: "Telegram · Анна" } }), createDraft: vi.fn(), editDraft: vi.fn().mockResolvedValue({}), addMessage: vi.fn(), editMessage: vi.fn().mockResolvedValue({}), deleteMessage: vi.fn().mockResolvedValue({}), generateDraft: vi.fn().mockResolvedValue({}), cancelDraft: vi.fn(), getModels: vi.fn(), setModel: vi.fn(), setConversationNote: vi.fn().mockResolvedValue({}) },
  telegramClient: { listBots: vi.fn().mockResolvedValue({ bots: [{ id: "bot", username: "helper", businessCanReply: true, sendDelaySeconds: 5 }] }), sendDraft: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers(); });

const incoming = new AcpMessage({
  id: "incoming", author: "contact", content: "Привет", source: "telegram-business/bot/conn/1",
  delivery: "received", createdAt: BigInt(Math.floor(Date.now() / 1000)),
});
const perform = async (_name: string, action: () => Promise<unknown>) => { await action(); return true; };

test("expired business reply offers to copy the draft", async () => {
  const expired = new AcpMessage({ ...incoming, createdAt: incoming.createdAt - 86401n });
  const draft = new AcpMessage({ id: "draft", author: "agent", content: "Ответ", delivery: "draft", replyToId: expired.id });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[expired, draft]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  expect(await screen.findByText(/Скопируйте черновик и отправьте его в Telegram/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Скопировать" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Отправить" })).toBeNull();
});

test("manual reply is opened explicitly, waits five seconds, and can be cancelled", async () => {
  vi.useFakeTimers();
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  expect(screen.queryByRole("textbox", { name: "Ручной ответ" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Написать вручную" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Ручной ответ" }), { target: { value: "Добрый день" } });
  fireEvent.click(screen.getByRole("button", { name: "Отправить" }));
  expect(screen.getByText(/Отправим через 5 с/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Отменить" }));
  await act(async () => { vi.advanceTimersByTime(6000); });
  expect(telegramClient.sendDraft).not.toHaveBeenCalled();
  expect((screen.getByRole("textbox", { name: "Ручной ответ" }) as HTMLTextAreaElement).value).toBe("Добрый день");
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

test("generates a draft with an optional instruction and the selected reply target", async () => {
  const target = new AcpMessage({ ...incoming, id: "target", content: "Нужен статус заказа" });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming, target]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  fireEvent.click(screen.getAllByRole("button", { name: "Ответить на это сообщение" })[1]);
  fireEvent.change(screen.getByRole("textbox", { name: "Инструкция для черновика" }), { target: { value: "Вежливо уточни номер заказа" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Сгенерировать" })); });
  expect(sessionClient.generateDraft).toHaveBeenCalledWith({
    sessionId: "session",
    instruction: "Вежливо уточни номер заказа",
    replyToMessageId: "target",
    selectedMessageIds: expect.arrayContaining(["target"]),
  });
});

test("stores the conversation note separately from the visible message history", async () => {
  const contactNote = new AcpMessage({ id: "contact-note", author: "owner", content: "Предпочитает короткие ответы", source: "brigade:contact-note", delivery: "received", createdAt: BigInt(Math.floor(Date.now() / 1000)) });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming, contactNote]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  expect(screen.queryByText("Заметка · видят только вы и агент")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Развернуть заметку о собеседнике" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Заметка о собеседнике" }), { target: { value: "Предпочитает созвоны утром" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Сохранить" })); });
  expect(sessionClient.setConversationNote).toHaveBeenCalledWith({ sessionId: "session", content: "Предпочитает созвоны утром" });
});

test("draft is edited inside its card before it can be sent", async () => {
  const draft = new AcpMessage({ id: "draft", author: "agent", content: "Черновой ответ", delivery: "draft", replyToId: incoming.id });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[incoming, draft]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  fireEvent.click(screen.getByRole("button", { name: "Изменить" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Текст черновика" }), { target: { value: "Исправленный ответ" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Сохранить" })); });
  expect(sessionClient.editDraft).toHaveBeenCalledWith({ sessionId: "session", messageId: "draft", content: "Исправленный ответ" });
});

test("loads a business photo only after the user requests it and keeps its caption", async () => {
  const photo = new AcpMessage({ ...incoming, content: "[Вложение Telegram: telegram-123-photo.jpg] Подпись к фото", hasTelegramBusinessPhoto: true });
  render(<MemoryRouter><BusinessConversation sessionId="session" messages={[photo]} run={null} busy="" loadError={false} act={perform} /></MemoryRouter>);
  await act(async () => {});
  expect(screen.getByRole("button", { name: "Показать фото" })).toBeTruthy();
  expect(screen.getByText("Подпись к фото")).toBeTruthy();
  expect(screen.queryByText(/Вложение Telegram/)).toBeNull();
});
