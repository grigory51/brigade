// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { sessionClient, telegramClient } from "@/api/client";
import { DraftComposer } from "./DraftComposer";

vi.mock("@/api/client", () => ({
  sessionClient: { editDraft: vi.fn() },
  telegramClient: { sendDraft: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn() } }));

afterEach(() => { cleanup(); vi.clearAllMocks(); });

const draft = new AcpMessage({ id: "draft", content: "Исходный ответ", delivery: "draft" });
const incoming = new AcpMessage({ id: "incoming", author: "contact", content: "Привет", source: "telegram-business/bot/conn/1" });
const perform = async (_name: string, action: () => Promise<unknown>) => { await action(); return true; };

test("draft without a Business target explains why it cannot be sent", () => {
  render(<DraftComposer message={draft} sessionId="session" replyTarget={null} latestContact={null} busy="" act={perform} />);
  expect(screen.getByText(/Нет входящего Business-сообщения/)).toBeTruthy();
  expect((screen.getByRole("button", { name: "Отправить в Telegram" }) as HTMLButtonElement).disabled).toBe(true);
});

test("edited draft is saved and explicitly confirmed before sending to the selected target", async () => {
  vi.mocked(sessionClient.editDraft).mockResolvedValue({} as never);
  vi.mocked(telegramClient.sendDraft).mockResolvedValue({} as never);
  render(<DraftComposer message={draft} sessionId="session" replyTarget={incoming} latestContact={incoming} busy="" act={perform} />);
  fireEvent.change(screen.getByRole("textbox", { name: "Текст ответа" }), { target: { value: "Новый ответ" } });
  fireEvent.click(screen.getByRole("button", { name: "Отправить в Telegram" }));
  expect(telegramClient.sendDraft).not.toHaveBeenCalled();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Да, отправить" })); });
  expect(sessionClient.editDraft).toHaveBeenCalledWith({ sessionId: "session", messageId: "draft", content: "Новый ответ" });
  expect(telegramClient.sendDraft).toHaveBeenCalledWith({ sessionId: "session", messageId: "draft", replyToMessageId: "incoming" });
});
