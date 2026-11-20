// @vitest-environment happy-dom
import { cleanup, fireEvent, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { VirtuosoMockContext } from "react-virtuoso";
import { afterEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { ArchivePage, ArchiveSessionPage } from "./ArchivePage";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { useArchivedRuntime } from "@/features/acp/useArchivedRuntime";

const { list, getHistory } = vi.hoisted(() => ({ list: vi.fn(), getHistory: vi.fn() }));
vi.mock("@/api/client", () => ({ archiveClient: { list, getHistory } }));
vi.mock("@/features/acp/AcpThread", () => ({ AcpThread: () => null }));
vi.mock("@/features/acp/useArchivedRuntime", () => ({ useArchivedRuntime: vi.fn() }));
vi.mock("@/features/acp/dock/SessionDock", () => ({ SessionDock: () => null }));

afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("empty archive uses an empty state without a redundant page header", async () => {
  list.mockResolvedValue({ sessions: [] });
  const view = render(<MemoryRouter><ArchivePage /></MemoryRouter>);
  expect(await view.findByRole("heading", { name: "Архив пока пуст" })).toBeTruthy();
  expect(view.queryByRole("heading", { name: "Архив" })).toBeNull();
});

test("archive with sessions retains its header and entries", async () => {
  list.mockResolvedValue({ sessions: [new Session({ id: "archived", name: "Проект" })] });
  const view = render(<MemoryRouter><ArchivePage /></MemoryRouter>);
  expect(await view.findByRole("heading", { name: "Архив" })).toBeTruthy();
  expect(view.getByText("Проект")).toBeTruthy();
  expect(view.queryByText("Архив пока пуст")).toBeNull();
});

test("a large Telegram archive opens at the latest message and scrolls back without mounting the full snapshot", async () => {
  getHistory.mockResolvedValue({ messages: Array.from({ length: 1003 }, (_, index) => new AcpMessage({
    id: String(index), content: `Сообщение №${index + 1}`, role: "user",
    author: index % 2 ? "contact" : "owner", source: "telegram-business/bot/conn/chat",
  })) });
  const geometry = { viewportHeight: 400, itemHeight: 40 };
  const view = render(<VirtuosoMockContext.Provider value={geometry}><MemoryRouter initialEntries={["/archive/test"]}><Routes>
    <Route path="/archive/:sessionId" element={<ArchiveSessionPage />} />
  </Routes></MemoryRouter></VirtuosoMockContext.Provider>);
  expect(await view.findByText("Сообщение №1003")).toBeTruthy();
  expect(view.queryByText("Сообщение №1")).toBeNull();
  expect((await view.findAllByRole("listitem")).length).toBeLessThan(30);
  const scroller = view.getByRole("list", { name: "Архив переписки" });
  Object.defineProperties(scroller, {
    offsetHeight: { value: geometry.viewportHeight },
    scrollHeight: { value: 1003 * geometry.itemHeight },
  });
  fireEvent.scroll(scroller, { target: { scrollTop: 0 } });
  expect(await view.findByText("Сообщение №1")).toBeTruthy();
  expect(view.queryByText("Сообщение №1003")).toBeNull();
  expect(view.getAllByRole("listitem").length).toBeLessThan(30);
  expect(getHistory).toHaveBeenCalledTimes(1);
  expect(useArchivedRuntime).not.toHaveBeenCalled();
});
