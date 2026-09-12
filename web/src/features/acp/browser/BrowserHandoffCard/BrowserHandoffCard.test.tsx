// @vitest-environment happy-dom
import { fireEvent, render, cleanup, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { BrowserHandoffCard, browserRequestId } from "./BrowserHandoffCard";

const { interact, append } = vi.hoisted(() => ({ interact: vi.fn(), append: vi.fn() }));
vi.mock("@/api/client", () => ({ browserClient: { interact } }));
vi.mock("@assistant-ui/react", () => ({ useThreadRuntime: () => ({ append }), useAuiState: () => false }));
vi.mock("../BrowserWindow/BrowserWindow", () => ({ BrowserWindow: ({ open, onFinish }: { open: boolean; onFinish: (cancel: boolean) => void }) => open ? <button onClick={() => onFinish(false)}>Продолжить</button> : null }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
const id = "3d1f7763-1c63-4407-b1e2-9edb5425c5d5";

test("extracts handoff IDs from plain and MCP-wrapped results", () => {
  const result = { browserRequestId: id };
  expect(browserRequestId(JSON.stringify(result))).toBe(id);
  expect(browserRequestId({ content: [{ text: JSON.stringify(result) }] })).toBe(id);
  expect(browserRequestId(undefined)).toBeUndefined();
});

test("restores a pending card and resumes without credentials in the prompt", async () => {
  interact.mockResolvedValueOnce({ state: "human" }).mockResolvedValueOnce({ state: "agent" });
  append.mockResolvedValue(undefined);
  const view = render(<BrowserHandoffCard sessionId="session" result={{ browserRequestId: id }} reason="Войдите на сайте" />);
  await waitFor(() => expect(interact).toHaveBeenCalledTimes(1));
  fireEvent.click(view.getByRole("button", { name: "Открыть браузер" }));
  fireEvent.click(view.getByRole("button", { name: "Продолжить" }));
  await waitFor(() => expect(append).toHaveBeenCalledTimes(1));
  expect(append.mock.calls[0][0].content[0].text).toContain("browser/read");
  expect(view.getByText("Управление возвращено агенту.")).toBeTruthy();
});

test("expired cards explain why the browser cannot be opened", async () => {
  interact.mockResolvedValue({ state: "expired" });
  const view = render(<BrowserHandoffCard sessionId="session" result={{ browserRequestId: id }} />);
  await view.findByText(/Браузер закрыт или запрос устарел/);
  expect(view.queryByRole("button", { name: "Открыть браузер" })).toBeNull();
});
