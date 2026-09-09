// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { ArchivePage } from "./ArchivePage";

const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api/client", () => ({ archiveClient: { list } }));
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
