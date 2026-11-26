// @vitest-environment happy-dom
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ConfigOption } from "@/features/acp/useAcpRuntime";
import { ConfigOptionSelect } from "./config-option";

afterEach(cleanup);

const thoughtLevel: ConfigOption = {
  id: "effort",
  category: "thought_level",
  name: "Thinking effort",
  currentValue: "medium",
  options: [
    { value: "medium", name: "Medium" },
    { value: "high", name: "High" },
    { value: "vendor", name: "Vendor adaptive", description: "Provider-specific behavior" },
  ],
};

test("localizes standard values but keeps vendor names, descriptions and wire values", async () => {
  const onChange = vi.fn();
  const view = render(<ConfigOptionSelect option={thoughtLevel} onChange={onChange} />);
  const trigger = view.getByRole("button", { name: "Глубина рассуждений: Средняя" });
  expect(trigger.textContent).toBe("Средняя");
  fireEvent.keyDown(trigger, { key: "ArrowDown" });
  expect(await view.findByRole("menuitem", { name: "Высокая" })).toBeTruthy();
  fireEvent.click(view.getByRole("menuitem", { name: "Vendor adaptive Provider-specific behavior" }));
  await waitFor(() => expect(onChange).toHaveBeenCalledWith("effort", "vendor"));
});

test("model only shows its name, including names that match standard effort values", () => {
  const view = render(<ConfigOptionSelect option={{ ...thoughtLevel, category: "model" }} onChange={() => {}} />);
  expect(view.getByRole("button", { name: "Модель: Medium" }).textContent).toBe("Medium");
  expect(view.queryByText("Model:")).toBeNull();
});

test("supports dock pills, external classes and disabled mode controls", () => {
  const view = render(<ConfigOptionSelect option={{ ...thoughtLevel, category: "mode", currentValue: "default", options: [{ value: "default", name: "Default" }] }} onChange={() => {}} variant="dock" className="parent-chip" disabled />);
  const button = view.getByRole("button", { name: "Права агента: По умолчанию" }) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  expect(button.classList.contains("rounded-full")).toBe(true);
  expect(button.classList.contains("parent-chip")).toBe(true);
  expect(button.hasAttribute("data-dock-chip")).toBe(true);
  expect(button.querySelector(".lucide-shield")).toBeTruthy();
});

test.each([
  ["Approve for me", "Автоодобрение"],
  ["Ask me", "С подтверждением"],
  ["Ask before edits", "С подтверждением"],
  ["bypassPermissions", "Без запросов разрешений"],
  ["acceptEdits", "Разрешать изменения"],
])("localizes the permission mode %s", (name, label) => {
  const option: ConfigOption = { id: "mode", category: "mode", name: "Mode", currentValue: "vendor-id", options: [{ value: "vendor-id", name }] };
  const view = render(<ConfigOptionSelect option={option} onChange={() => {}} variant="dock" />);
  expect(view.getByRole("button", { name: `Права агента: ${label}` }).textContent).toBe(label);
});

test("keeps an unknown current value visible even when absent from options", () => {
  const view = render(<ConfigOptionSelect option={{ ...thoughtLevel, currentValue: "vendor-future" }} onChange={() => {}} />);
  expect(view.getByRole("button", { name: "Глубина рассуждений: vendor-future" })).toBeTruthy();
});

test("shows pending until completion without optimistic selection or errors on resolved callbacks", async () => {
  let finish!: () => void;
  const onChange = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  const view = render(<ConfigOptionSelect option={thoughtLevel} onChange={onChange} />);
  const button = view.getByRole("button", { name: "Глубина рассуждений: Средняя" }) as HTMLButtonElement;
  fireEvent.keyDown(button, { key: "ArrowDown" });
  fireEvent.click(await view.findByRole("menuitem", { name: "Высокая" }));
  expect(button.disabled).toBe(true);
  expect(button.getAttribute("aria-busy")).toBe("true");
  expect(view.getByRole("status").textContent).toContain("сохранение");
  expect(button.textContent).toBe("Средняя");
  await act(async () => finish());
  expect(button.disabled).toBe(false);
  expect(view.queryByRole("alert")).toBeNull();
  expect(button.textContent).toBe("Средняя");
  view.rerender(<ConfigOptionSelect option={{ ...thoughtLevel, currentValue: "high" }} onChange={onChange} />);
  expect(view.getByRole("button", { name: "Глубина рассуждений: Высокая" })).toBeTruthy();
});

test.each(["throw", "reject"])("reports %s locally and allows retry", async (failure) => {
  const onChange = vi.fn(() => {
    if (failure === "throw") throw new Error("Недоступно");
    return Promise.reject(new Error("Недоступно"));
  });
  const view = render(<ConfigOptionSelect option={thoughtLevel} onChange={onChange} />);
  const button = view.getByRole("button", { name: "Глубина рассуждений: Средняя" }) as HTMLButtonElement;
  fireEvent.keyDown(button, { key: "ArrowDown" });
  fireEvent.click(await view.findByRole("menuitem", { name: "Высокая" }));
  expect((await view.findByRole("alert")).textContent).toContain("Недоступно");
  expect(button.disabled).toBe(false);
  expect(button.getAttribute("aria-busy")).toBe("false");
});

test("completion of an unmounted option does not clear another option's pending state", async () => {
  let finishFirst!: () => void;
  let finishSecond!: () => void;
  const first = () => new Promise<void>((resolve) => { finishFirst = resolve; });
  const second = () => new Promise<void>((resolve) => { finishSecond = resolve; });
  const view = render(<ConfigOptionSelect option={thoughtLevel} onChange={first} />);
  fireEvent.keyDown(view.getByRole("button"), { key: "ArrowDown" });
  fireEvent.click(await view.findByRole("menuitem", { name: "Высокая" }));
  view.rerender(<ConfigOptionSelect option={{ ...thoughtLevel, id: "another" }} onChange={second} />);
  fireEvent.keyDown(view.getByRole("button"), { key: "ArrowDown" });
  fireEvent.click(await view.findByRole("menuitem", { name: "Высокая" }));
  await act(async () => finishFirst());
  expect(view.getByRole("button").getAttribute("aria-busy")).toBe("true");
  await act(async () => finishSecond());
  expect(view.getByRole("button").getAttribute("aria-busy")).toBe("false");
});
