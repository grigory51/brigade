// @vitest-environment happy-dom
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { SessionTitle } from "./SessionTitle";

const { renameSession } = vi.hoisted(() => ({ renameSession: vi.fn() }));
vi.mock("./SessionLayout", () => ({ useSessionShell: () => ({ activeSession: undefined, renameSession, renamingIds: new Set<string>() }) }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("renames through the existing session owner and shows pending until confirmation", async () => {
  let done!: (success: boolean) => void;
  renameSession.mockReturnValue(new Promise<boolean>((resolve) => { done = resolve; }));
  const view = render(<SessionTitle session={new Session({ id: "s", name: "test" })} />);
  fireEvent.click(view.getByRole("button", { name: "Переименовать сессию: test" }));
  const input = view.getByRole("textbox", { name: "Название сессии" });
  fireEvent.change(input, { target: { value: "  Новый проект  " } });
  fireEvent.submit(input.closest("form")!);
  expect(renameSession).toHaveBeenCalledWith("s", "Новый проект");
  expect((input as HTMLInputElement).disabled).toBe(true);
  await act(async () => done(true));
  expect(view.queryByRole("textbox")).toBeNull();
});

test("failed rename preserves input and Escape cancels without a request", async () => {
  renameSession.mockResolvedValue(false);
  const view = render(<SessionTitle session={new Session({ id: "s", name: "test" })} />);
  fireEvent.click(view.getByRole("button", { name: "Переименовать сессию: test" }));
  const input = view.getByRole("textbox");
  fireEvent.change(input, { target: { value: "retry" } });
  fireEvent.submit(input.closest("form")!);
  await waitFor(() => expect((input as HTMLInputElement).disabled).toBe(false));
  expect((input as HTMLInputElement).value).toBe("retry");
  fireEvent.keyDown(input, { key: "Escape" });
  expect(view.queryByRole("textbox")).toBeNull();
  expect(renameSession).toHaveBeenCalledTimes(1);
});
