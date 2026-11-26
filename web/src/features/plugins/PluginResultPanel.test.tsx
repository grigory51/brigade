// @vitest-environment happy-dom
// @vitest-environment-options {"settings":{"navigation":{"disableChildFrameNavigation":true}}}
import { useEffect } from "react";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { PluginResultPanel } from "./PluginResultPanel";
import type { PluginResult } from "./host-tools";

const { mounted, unmounted } = vi.hoisted(() => ({ mounted: vi.fn(), unmounted: vi.fn() }));
vi.mock("@/features/terminal/TerminalView", () => ({
  TerminalView: ({ command, onCommandExit }: { command: { argv: string[] }; onCommandExit: (code: number) => void }) => {
    useEffect(() => { mounted(); return () => { unmounted(); }; }, []);
    return <div><button onClick={() => onCommandExit(7)}>{command.argv.join(" ")}</button><button onClick={() => onCommandExit(0)}>Успех</button><pre>Command output</pre></div>;
  },
}));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
const terminal: PluginResult = { name: "Tests", kind: "terminal", command: { argv: ["npm", "test"], cwd: "app" } };

test("terminal requires explicit launch and displays actual exit code", () => {
  const view = render(<PluginResultPanel sessionId="s" result={terminal} revision={1} />);
  expect(mounted).not.toHaveBeenCalled();
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  expect(mounted).toHaveBeenCalledTimes(1);
  fireEvent.click(view.getByRole("button", { name: "npm test" }));
  expect(view.getByRole("status").textContent).toContain("exit code 7");
  view.unmount();
  const reloaded = render(<PluginResultPanel sessionId="s" result={terminal} revision={1} />);
  expect(reloaded.getByRole("button", { name: "Запустить" })).toBeTruthy();
  expect(mounted).toHaveBeenCalledTimes(1);
});

test.each([0, 7])("terminal result preserves output and exit code %i without remounting or replay", (exitCode) => {
  const view = render(<PluginResultPanel sessionId="s" result={terminal} revision={1} />);
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  const output = view.getByText("Command output");
  fireEvent.click(view.getByRole("button", { name: exitCode === 0 ? "Успех" : "npm test" }));
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  view.rerender(<PluginResultPanel sessionId="s" result={{ ...terminal }} revision={2} />);
  expect(view.getByText("Command output")).toBe(output);
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  expect(view.queryByRole("button", { name: "Запустить" })).toBeNull();
  expect(mounted).toHaveBeenCalledOnce();
  expect(unmounted).not.toHaveBeenCalled();
  view.unmount();
  expect(unmounted).toHaveBeenCalledOnce();
});

test("new terminal revision preserves the running shell until an explicit restart", () => {
  const view = render(<PluginResultPanel sessionId="s" result={terminal} revision={1} />);
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  const next: PluginResult = { ...terminal, kind: "terminal", command: { argv: ["npm", "run", "dev"], cwd: "app" } };
  view.rerender(<PluginResultPanel sessionId="s" result={next} revision={2} />);
  expect(view.getByRole("button", { name: "npm test" })).toBeTruthy();
  expect(unmounted).not.toHaveBeenCalled();
  fireEvent.click(view.getByRole("button", { name: "Перезапустить с новой командой" }));
  expect(view.getByRole("button", { name: "npm run dev" })).toBeTruthy();
  expect(unmounted).toHaveBeenCalledTimes(1);
});

test("same-origin preview is external-only, never scripts plus same-origin iframe", () => {
  const result: PluginResult = { name: "App", kind: "web", url: `${window.location.origin}/preview/s/3000/` };
  const view = render(<PluginResultPanel sessionId="s" result={result} revision={1} />);
  expect(view.container.querySelector("iframe")).toBeNull();
  expect(view.getByRole("link", { name: "Открыть внешне" }).getAttribute("href")).toBe(result.url);
});

test("changing terminal result kind does not terminate a running shell without consent", () => {
  const view = render(<PluginResultPanel sessionId="s" result={terminal} revision={1} />);
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  const web: PluginResult = { name: "Tests", kind: "web", url: "https://preview.test/" };
  view.rerender(<PluginResultPanel sessionId="s" result={web} revision={2} />);
  expect(view.getByRole("button", { name: "npm test" })).toBeTruthy();
  expect(unmounted).not.toHaveBeenCalled();
  fireEvent.click(view.getByRole("button", { name: "Закрыть shell и открыть preview" }));
  expect(view.getByTitle("Tests")).toBeTruthy();
  expect(unmounted).toHaveBeenCalledOnce();
});

test("isolated preview supports modules and quietly refreshes on a new revision", () => {
  const result: PluginResult = { name: "App", kind: "web", url: "https://preview.test/" };
  const view = render(<PluginResultPanel sessionId="s" result={result} revision={1} />);
  const frame = view.getByTitle("App");
  expect(frame.getAttribute("sandbox")).toContain("allow-same-origin");
  expect(view.getByRole("status")).toBeTruthy();
  fireEvent.load(frame);
  expect(view.queryByRole("status")).toBeNull();
  view.rerender(<PluginResultPanel sessionId="s" result={result} revision={2} />);
  expect(view.getByTitle("App")).not.toBe(frame);
  expect(view.queryByRole("status")).toBeNull();
});
