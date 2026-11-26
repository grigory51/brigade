// @vitest-environment happy-dom
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Session, SessionKind } from "@/api/gen/brigade/v1/session_pb";
import { SessionLayout, useSessionShell } from "./SessionLayout";

const { list, update, markRead, error } = vi.hoisted(() => ({
  list: vi.fn(), update: vi.fn(), markRead: vi.fn(), error: vi.fn(),
}));
vi.mock("@/api/client", () => ({ sessionClient: { list, update, markRead } }));
vi.mock("sonner", () => ({ toast: { error } }));
vi.mock("@/features/auth/AuthContext", () => ({
  useAuth: () => ({ desktop: true, user: { id: "user" } }),
}));
vi.mock("@/features/terminal/ShellPanel", () => ({ ShellPanel: () => null }));
vi.mock("@/features/desktop/EnvironmentSwitcher", () => ({ EnvironmentSwitcher: () => null }));
vi.mock("./CreateSessionDialog", () => ({ CreateSessionDialog: ({ open }: { open: boolean }) => open ? <div role="dialog" aria-label="Новая сессия" /> : null }));

let shell: ReturnType<typeof useSessionShell>;
function ShellProbe() {
  shell = useSessionShell();
  return null;
}

function renderLayout() {
  return render(
    <MemoryRouter initialEntries={["/s/first"]} future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
      <Routes>
        <Route path="/s/:sessionId" element={<SessionLayout />}>
          <Route index element={<ShellProbe />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  list.mockResolvedValue({ sessions: [
    new Session({ id: "first", name: "Original", kind: SessionKind.ACP }),
    new Session({ id: "second", name: "Other", kind: SessionKind.ACP }),
  ] });
  markRead.mockResolvedValue({});
});
afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("uses Alt+N without intercepting the browser Cmd+N shortcut", async () => {
  const view = renderLayout();
  await waitFor(() => expect(shell.activeSession?.name).toBe("Original"));
  fireEvent.keyDown(window, { code: "KeyN", key: "n", metaKey: true });
  expect(view.queryByRole("dialog", { name: "Новая сессия" })).toBeNull();
  fireEvent.keyDown(window, { code: "KeyN", key: "т", altKey: true });
  expect(view.getByRole("dialog", { name: "Новая сессия" })).toBeTruthy();
  expect(view.getByRole("button", { name: /Новая сессия/ }).getAttribute("aria-keyshortcuts")).toBe("Alt+N");
});

test("rejects same-tick overlapping renames, rolls back failure, and permits a later retry", async () => {
  let reject!: (reason: Error) => void;
  update.mockReturnValueOnce(new Promise((_, fail) => { reject = fail; }));
  const view = renderLayout();
  await waitFor(() => expect(shell.activeSession?.name).toBe("Original"));

  let first!: Promise<boolean>;
  await act(async () => {
    const rename = shell.renameSession;
    first = rename("first", "Older edit");
    expect(await rename("first", "Newer edit")).toBe(false);
  });
  expect(update).toHaveBeenCalledTimes(1);
  expect(shell.renamingIds.has("first")).toBe(true);
  expect(shell.activeSession?.name).toBe("Older edit");
  for (const name of ["Переименовать сессию", "Удалить сессию", "Архивировать сессию"]) {
    expect((view.getAllByRole("button", { name })[0] as HTMLButtonElement).disabled).toBe(true);
  }
  fireEvent.doubleClick(view.getByRole("button", { name: /Older edit/ }));
  expect(view.queryByRole("textbox", { name: "Название сессии" })).toBeNull();

  await act(async () => {
    reject(new Error("rename failed"));
    expect(await first).toBe(false);
  });
  expect(shell.activeSession?.name).toBe("Original");
  expect(shell.renamingIds.size).toBe(0);
  expect(error).toHaveBeenCalledTimes(1);

  update.mockResolvedValueOnce({});
  await act(async () => { expect(await shell.renameSession("first", "Newer edit")).toBe(true); });
  expect(shell.activeSession?.name).toBe("Newer edit");
  expect(shell.renamingIds.size).toBe(0);
  expect(update).toHaveBeenCalledTimes(2);
});

test("keeps locks independent across sessions and clears each after success", async () => {
  let resolveFirst!: (value: object) => void;
  let resolveSecond!: (value: object) => void;
  update
    .mockReturnValueOnce(new Promise((done) => { resolveFirst = done; }))
    .mockReturnValueOnce(new Promise((done) => { resolveSecond = done; }));
  renderLayout();
  await waitFor(() => expect(shell.activeSession?.name).toBe("Original"));

  let first!: Promise<boolean>;
  let second!: Promise<boolean>;
  await act(async () => {
    first = shell.renameSession("first", "First edit");
    second = shell.renameSession("second", "Second edit");
  });
  expect([...shell.renamingIds]).toEqual(["first", "second"]);
  expect(update).toHaveBeenCalledTimes(2);
  await act(async () => { resolveFirst({}); expect(await first).toBe(true); });
  expect([...shell.renamingIds]).toEqual(["second"]);
  await act(async () => { resolveSecond({}); expect(await second).toBe(true); });
  expect(shell.renamingIds.size).toBe(0);
});

test("makes an already open sidebar editor read-only during a rename from the shell", async () => {
  let resolve!: (value: object) => void;
  update.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  const view = renderLayout();
  await waitFor(() => expect(shell.activeSession?.name).toBe("Original"));
  fireEvent.click(view.getAllByRole("button", { name: "Переименовать сессию" })[0]);
  const input = view.getByRole("textbox", { name: "Название сессии" }) as HTMLInputElement;
  fireEvent.change(input, { target: { value: "Sidebar draft" } });

  let request!: Promise<boolean>;
  await act(async () => { request = shell.renameSession("first", "Header edit"); });
  expect(input.readOnly).toBe(true);
  fireEvent.keyDown(input, { key: "Enter" });
  fireEvent.blur(input);
  expect(update).toHaveBeenCalledTimes(1);
  await act(async () => { resolve({}); expect(await request).toBe(true); });
  expect(input.readOnly).toBe(false);
  expect(input.value).toBe("Sidebar draft");
  expect(shell.activeSession?.name).toBe("Header edit");
});
