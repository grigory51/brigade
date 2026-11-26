// @vitest-environment happy-dom
import { cleanup, fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { SessionPalette } from "./SessionPalette";

afterEach(cleanup);

test("creates a session from the palette without selecting an existing session", async () => {
  const onCreate = vi.fn();
  const onSelect = vi.fn();
  const view = render(<SessionPalette sessions={[]} state="ready" onSelect={onSelect} onCreate={onCreate} />);
  fireEvent.keyDown(window, { key: "k", metaKey: true });
  const dialog = await view.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("option", { name: "Новая сессия" }));
  expect(onCreate).toHaveBeenCalledOnce();
  expect(onSelect).not.toHaveBeenCalled();
});

test("opens with Cmd+K, searches previews and selects sessions with duplicate names", async () => {
  const sessions = [
    new Session({ id: "first", name: "test", lastMessage: "Первый проект" }),
    new Session({ id: "second", name: "test", lastMessage: "Редактор моделей" }),
  ];
  const onSelect = vi.fn();
  const view = render(<SessionPalette sessions={sessions} state="ready" onSelect={onSelect} />);
  expect(view.queryByRole("combobox")).toBeNull();
  fireEvent.keyDown(window, { key: "k", code: "KeyK", metaKey: true });
  const dialog = await view.findByRole("dialog", { name: "Перейти к сессии" });
  const input = within(dialog).getByRole("combobox");
  fireEvent.change(input, { target: { value: "моделей" } });
  await waitFor(() => expect(within(dialog).getAllByRole("option")).toHaveLength(1));
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(onSelect).toHaveBeenCalledWith(sessions[1]));
  await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
});

test("Ctrl+K toggles the palette, ignores repeat and composing events", async () => {
  const view = render(<SessionPalette sessions={[]} state="ready" onSelect={vi.fn()} />);
  fireEvent.keyDown(window, { key: "k", ctrlKey: true, repeat: true });
  fireEvent.keyDown(window, { key: "k", ctrlKey: true, isComposing: true });
  expect(view.queryByRole("dialog")).toBeNull();
  fireEvent.keyDown(window, { key: "л", code: "KeyK", ctrlKey: true });
  await view.findByRole("dialog");
  expect(view.getByText("Сессии не найдены.")).toBeTruthy();
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  await waitFor(() => expect(view.queryByRole("dialog")).toBeNull());
});

test("loading and failure are distinct from no results", async () => {
  const view = render(<SessionPalette sessions={[]} state="loading" onSelect={vi.fn()} />);
  fireEvent.click(view.getByRole("button", { name: "Поиск сессий" }));
  expect(await view.findByRole("progressbar")).toBeTruthy();
  expect(view.queryByText("Сессии не найдены.")).toBeNull();
  view.rerender(<SessionPalette sessions={[]} state="error" onSelect={vi.fn()} />);
  expect(view.getByRole("alert").textContent).toBe("Не удалось загрузить сессии.");
  expect(view.queryByRole("progressbar")).toBeNull();
});
