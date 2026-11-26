// @vitest-environment happy-dom
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { TerminalView, terminalCommandInput } from "@/features/terminal/TerminalView";
import { PluginResultPanel } from "./PluginResultPanel";

const { term, streamUrl } = vi.hoisted(() => ({
  term: {
    loadAddon: vi.fn(), open: vi.fn(), focus: vi.fn(), dispose: vi.fn(), write: vi.fn(),
    getSelection: vi.fn(() => ""), attachCustomKeyEventHandler: vi.fn(),
    onSelectionChange: vi.fn(() => ({ dispose: vi.fn() })),
    onData: vi.fn(() => ({ dispose: vi.fn() })),
    parser: { registerOscHandler: vi.fn(() => ({ dispose: vi.fn() })) },
    cols: 80, rows: 24,
  },
  streamUrl: vi.fn(async () => "ws://localhost/shell"),
}));
vi.mock("@xterm/xterm", () => ({ Terminal: class { constructor() { return term; } } }));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@/api/ws", () => ({ streamUrl }));

class Socket {
  static OPEN = 1;
  static instances: Socket[] = [];
  readyState = 1;
  binaryType = "";
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  send = vi.fn();
  close = vi.fn();
  constructor() { Socket.instances.push(this); }
}

beforeEach(() => {
  Socket.instances = [];
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
});
afterEach(() => { cleanup(); vi.clearAllMocks(); term.open.mockReset(); term.write.mockReset(); vi.unstubAllGlobals(); });

test("quotes cwd and each argv without interpreting shell substitutions", () => {
  const input = terminalCommandInput({ cwd: "project's folder", argv: ["printf", "$(touch /tmp/never)", "a; echo unsafe", "'quoted'"] }, "test-marker");
  expect(input).toContain("'project'\\''s folder'");
  expect(input).toContain("'$(touch /tmp/never)'");
  expect(input).toContain("'a; echo unsafe'");
  expect(input).toContain("777;test-marker;exit;");
  expect(input.endsWith("\r")).toBe(true);
  expect(() => terminalCommandInput({ cwd: ".", argv: [] }, "marker")).toThrow();
  expect(() => terminalCommandInput({ cwd: ".", argv: ["cmd\0"] }, "marker")).toThrow();
});

test("sends the confirmed command once, never on reconnect, and reads wrapper exit status", async () => {
  const onCommandExit = vi.fn();
  const command = { argv: ["npm", "test"], cwd: "app" };
  const view = render(<TerminalView kind="shell" sessionId="s" command={command} onCommandExit={onCommandExit} />);
  await waitFor(() => expect(Socket.instances).toHaveLength(1));
  act(() => { Socket.instances[0].onopen?.(); });
  const inputs = Socket.instances[0].send.mock.calls.map(([message]) => JSON.parse(message as string) as { type: string; data: string }).filter((item) => item.type === "input");
  expect(inputs).toHaveLength(1);
  const marker = inputs[0].data.match(/777;([a-zA-Z0-9-]+);exit;/)![1];
  const register = term.parser.registerOscHandler as unknown as { mock: { calls: [number, (value: string) => boolean][] } };
  act(() => { register.mock.calls[0][1](`${marker};exit;7`); });
  expect(onCommandExit).toHaveBeenCalledWith(7);
  expect(Socket.instances[0].close).toHaveBeenCalledOnce();
  view.rerender(<TerminalView kind="shell" sessionId="s" command={command} onCommandExit={onCommandExit} attempt={1} />);
  await waitFor(() => expect(Socket.instances).toHaveLength(2));
  act(() => { Socket.instances[1].onopen?.(); });
  expect(Socket.instances[1].send.mock.calls.some(([message]) => JSON.parse(message as string).type === "input")).toBe(false);
  expect(Socket.instances[0].close).toHaveBeenCalledOnce();
  view.unmount();
  expect(Socket.instances[1].close).toHaveBeenCalledOnce();
});

test("ordinary session terminal ignores command exit markers", async () => {
  const view = render(<TerminalView kind="terminal" sessionId="s" />);
  await waitFor(() => expect(Socket.instances).toHaveLength(1));
  act(() => { Socket.instances[0].onopen?.(); });
  const register = term.parser.registerOscHandler as unknown as { mock: { calls: [number, (value: string) => boolean][] } };
  expect(register.mock.calls[0][1]("marker;exit;0")).toBe(false);
  expect(Socket.instances[0].close).not.toHaveBeenCalled();
  view.unmount();
  expect(Socket.instances[0].close).toHaveBeenCalledOnce();
});

test.each([0, 7])("result exit %i closes the socket but retains xterm and output without replay", async (exitCode) => {
  const output = document.createElement("pre");
  term.open.mockImplementationOnce((host: HTMLElement) => { host.append(output); });
  term.write.mockImplementation((data: string) => { output.textContent += data; });
  const result = { name: "Tests", kind: "terminal" as const, command: { argv: ["npm", "test"], cwd: "app" } };
  const view = render(<PluginResultPanel sessionId="s" result={result} revision={1} />);
  expect(Socket.instances).toHaveLength(0);
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  await waitFor(() => expect(Socket.instances).toHaveLength(1));
  const socket = Socket.instances[0];
  act(() => { socket.onopen?.(); });
  const inputs = socket.send.mock.calls.map(([message]) => JSON.parse(message as string) as { type: string; data: string }).filter((item) => item.type === "input");
  expect(inputs).toHaveLength(1);
  const marker = inputs[0].data.match(/777;([a-zA-Z0-9-]+);exit;/)![1];
  const register = term.parser.registerOscHandler as unknown as { mock: { calls: [number, (value: string) => boolean][] } };
  const text = `Command output\r\n[exit code ${exitCode}]\r\n`;
  act(() => {
    socket.onmessage?.({ data: text });
    register.mock.calls[0][1](`${marker};exit;${exitCode}`);
    socket.onclose?.();
  });
  expect(socket.close).toHaveBeenCalledOnce();
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  expect(view.container.contains(output)).toBe(true);
  expect(output.textContent).toBe(text);
  expect(term.dispose).not.toHaveBeenCalled();
  view.rerender(<PluginResultPanel sessionId="s" result={{ ...result, command: { ...result.command } }} revision={2} />);
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  expect(view.container.contains(output)).toBe(true);
  expect(output.textContent).toBe(text);
  expect(term.open).toHaveBeenCalledOnce();
  expect(term.dispose).not.toHaveBeenCalled();
  expect(streamUrl).toHaveBeenCalledOnce();
  expect(Socket.instances).toHaveLength(1);
  expect(socket.send.mock.calls.filter(([message]) => JSON.parse(message as string).type === "input")).toHaveLength(1);
  view.unmount();
  expect(term.dispose).toHaveBeenCalledOnce();
  expect(socket.close).toHaveBeenCalledOnce();
});
