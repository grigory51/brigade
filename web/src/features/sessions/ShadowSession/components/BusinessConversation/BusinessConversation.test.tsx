// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { telegramClient } from "@/api/client";
import { BusinessConversation } from "./BusinessConversation";

vi.mock("@/api/client", () => ({
  sessionClient: { get: vi.fn().mockResolvedValue({ session: { name: "Telegram · Анна" } }), createDraft: vi.fn(), editDraft: vi.fn(), addMessage: vi.fn(), generateDraft: vi.fn(), cancelDraft: vi.fn() },
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
