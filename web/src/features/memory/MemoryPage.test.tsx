// @vitest-environment happy-dom
import { cleanup, fireEvent, render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, expect, test, vi } from "vitest";
import { MemoryPage } from "./MemoryPage";
import { Topic } from "@/api/gen/brigade/v1/memory_pb";

const { listTopics } = vi.hoisted(() => ({ listTopics: vi.fn() }));
vi.mock("@/api/client", () => ({ memoryClient: { listTopics } }));

afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("unconfigured memory links directly to memory settings", async () => {
  listTopics.mockRejectedValue(new ConnectError("not configured", Code.FailedPrecondition));
  const view = render(<MemoryRouter><MemoryPage /></MemoryRouter>);
  expect((await view.findByRole("link", { name: "Настроить заметки" })).getAttribute("href")).toBe("/settings/memory");
  expect(view.queryByRole("heading", { level: 1, name: "Заметки" })).toBeNull();
  expect(view.queryByText(/SSH-ключ/)).toBeNull();
});

test("empty memory offers a working create action and hides onboarding while composing", async () => {
  listTopics.mockResolvedValue({ topics: [] });
  const view = render(<MemoryRouter><MemoryPage /></MemoryRouter>);
  const create = await view.findByRole("button", { name: "Создать тему" });
  expect(view.queryByRole("heading", { level: 1, name: "Заметки" })).toBeNull();
  fireEvent.click(create);
  expect(view.getByPlaceholderText("Название темы")).toBeTruthy();
  expect(view.queryByText("С какой темы начнём?")).toBeNull();
});

test("empty search can be cleared", async () => {
  listTopics.mockResolvedValue({ topics: [new Topic({ id: "topic", name: "Проект", initial: "П", color: "#c96442" })] });
  const view = render(<MemoryRouter><MemoryPage /></MemoryRouter>);
  const search = await view.findByRole("searchbox", { name: "Поиск по заметкам" });
  fireEvent.change(search, { target: { value: "missing" } });
  fireEvent.click(view.getByRole("button", { name: "Сбросить поиск" }));
  expect((search as HTMLInputElement).value).toBe("");
  expect(view.getByRole("link", { name: /Проект/ })).toBeTruthy();
  expect(view.getByRole("heading", { level: 1, name: "Заметки" })).toBeTruthy();
});

test("load failure is not presented as an empty repository and supports retry", async () => {
  listTopics.mockRejectedValueOnce(new ConnectError("Git недоступен", Code.Unavailable))
    .mockResolvedValueOnce({ topics: [] });
  const view = render(<MemoryRouter><MemoryPage /></MemoryRouter>);
  expect(await view.findByText("Git недоступен")).toBeTruthy();
  expect(view.queryByText("С какой темы начнём?")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Попробовать снова" }));
  expect(await view.findByText("С какой темы начнём?")).toBeTruthy();
});
