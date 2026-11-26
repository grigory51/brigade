// @vitest-environment happy-dom
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { MsgNav } from "./MsgNav";

vi.mock("@assistant-ui/react", () => ({
  useAuiState: (select: (state: unknown) => unknown) => select({ thread: { messages: [{ id: "one", role: "user", content: [{ type: "text", text: "Привет" }] }] } }),
}));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

test("navigation appears only while the conversation overflows and observes content growth", async () => {
  let resize!: () => void;
  const observe = vi.fn();
  vi.stubGlobal("ResizeObserver", class { constructor(callback: () => void) { resize = callback; } observe = observe; disconnect() {} });
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { callback(0); return 0; });
  const viewport = document.createElement("div");
  viewport.dataset.slot = "aui_thread-viewport";
  const content = document.createElement("div");
  viewport.appendChild(content);
  let height = 200;
  Object.defineProperties(viewport, { scrollHeight: { get: () => height }, clientHeight: { value: 300 } });
  document.body.appendChild(viewport);
  try {
    const view = render(<MsgNav />);
    expect(view.queryByLabelText("Навигация по диалогу")).toBeNull();
    expect(observe).toHaveBeenCalledWith(content);
    height = 500;
    await act(async () => resize());
    expect(view.getByLabelText("Навигация по диалогу")).toBeTruthy();
    height = 200;
    await act(async () => resize());
    expect(view.queryByLabelText("Навигация по диалогу")).toBeNull();
  } finally {
    viewport.remove();
  }
});
