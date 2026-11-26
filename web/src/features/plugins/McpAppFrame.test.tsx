// @vitest-environment happy-dom
// @vitest-environment-options {"settings":{"navigation":{"disableChildFrameNavigation":true}}}
import { useEffect } from "react";
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { OpenPluginResult } from "./host-tools";
import { McpAppFrame } from "./McpAppFrame";

const plugin = { id: "coder", version: "1", entryTool: "coder.open" };

const mocks = vi.hoisted(() => ({
  bridges: [] as { oncalltool: (params: { name: string; arguments?: Record<string, unknown> }) => Promise<CallToolResult>; oninitialized: () => void; sendToolResult: ReturnType<typeof vi.fn>; close: () => Promise<void> }[],
  bridgeOptions: [] as unknown[],
  mCP: vi.fn(), connect: vi.fn(), callPluginHostTool: vi.fn(), mounted: vi.fn(), unmounted: vi.fn(),
  thread: { append: vi.fn(), cancelRun: vi.fn() },
  composer: { getState: vi.fn(() => ({ text: "Existing draft" })), setText: vi.fn() },
}));
vi.mock("@/api/client", () => ({ pluginClient: { mCP: mocks.mCP } }));
vi.mock("@assistant-ui/react", () => ({
  useAui: () => ({ thread: mocks.thread, composer: mocks.composer }),
  useAuiState: (select: (state: { thread: { isRunning: boolean; messages: [] } }) => unknown) => select({ thread: { isRunning: false, messages: [] } }),
}));
vi.mock("@modelcontextprotocol/ext-apps/app-bridge", () => ({
  getToolUiResourceUri: () => "ui://app",
  PostMessageTransport: class {},
  AppBridge: class {
    oninitialized = () => {};
    oncalltool = async (): Promise<CallToolResult> => ({ content: [] });
    connect = mocks.connect;
    close = vi.fn(async () => {});
    sendToolInput = vi.fn(async () => {});
    sendToolResult = vi.fn(async () => {});
    constructor(...args: unknown[]) { mocks.bridges.push(this); mocks.bridgeOptions.push(args[3]); }
  },
}));
vi.mock("./host-tools", () => ({ callPluginHostTool: mocks.callPluginHostTool, pluginHostCapabilities: { openResult: true } }));
vi.mock("@/features/terminal/TerminalView", () => ({
  TerminalView: ({ onCommandExit }: { onCommandExit: (code: number) => void }) => {
    useEffect(() => { mocks.mounted(); return () => { mocks.unmounted(); }; }, []);
    return <div data-testid="terminal"><pre>Command output</pre>{[0, 7].map((code) => <button key={code} type="button" onClick={() => onCommandExit(code)}>exit {code}</button>)}</div>;
  },
}));

beforeEach(() => {
  mocks.bridges.length = 0;
  mocks.bridgeOptions.length = 0;
  mocks.connect.mockResolvedValue(undefined);
  mocks.mCP.mockImplementation(async ({ method }: { method: string }) => ({
    resultJson: new TextEncoder().encode(JSON.stringify(method === "tools/list" ? { tools: [{ name: "coder.open" }] } : method === "resources/read" ? { contents: [{ text: "<html><body>App</body></html>" }] } : { content: [] })),
  }));
  mocks.callPluginHostTool.mockImplementation(async (_session: string, params: { arguments: { revision: number; activate?: boolean } }, onOpen: (result: OpenPluginResult, activate?: boolean) => void) => {
    onOpen({ key: "result", revision: params.arguments.revision, result: { kind: "terminal", name: "Tests", command: { argv: ["npm", "test"], cwd: "." } } }, params.arguments.activate);
    return { content: [] };
  });
});
afterEach(() => { cleanup(); vi.clearAllMocks(); });

test("pane host advertises inline capabilities without duplicating the conversation", async () => {
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  await view.findByTitle("coder.open");
  await waitFor(() => expect(mocks.connect).toHaveBeenCalledOnce());
  mocks.bridges[0].oninitialized();
  expect(mocks.bridgeOptions[0]).toMatchObject({ hostContext: { displayMode: "inline", availableDisplayModes: ["inline"] } });
  expect(mocks.bridges[0].sendToolResult).toHaveBeenLastCalledWith({ content: [], structuredContent: { brigadeHost: { openResult: true } } });
  for (const [request, options] of mocks.mCP.mock.calls) {
    expect(request.expectedPlugin).toEqual({ id: "coder", version: "1" });
    expect(options.signal).toBeInstanceOf(AbortSignal);
  }
});

test("closing a loading pane aborts MCP and never continues initialization", async () => {
  let resolve!: (response: { resultJson: Uint8Array }) => void;
  mocks.mCP.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const signal = mocks.mCP.mock.calls[0][1].signal as AbortSignal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => { resolve({ resultJson: new TextEncoder().encode(JSON.stringify({ tools: [{ name: "coder.open" }] })) }); });
  expect(mocks.mCP).toHaveBeenCalledOnce();
  expect(mocks.connect).not.toHaveBeenCalled();
});

test("stable result updates do not duplicate tabs, steal focus or remount a terminal", async () => {
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const frame = await view.findByTitle("coder.open");
  await waitFor(() => expect(frame.getAttribute("srcdoc")).toContain("<body>App</body>"));
  await waitFor(() => expect(mocks.bridges).toHaveLength(1));
  const bridge = mocks.bridges[0];
  await act(async () => { await bridge.oncalltool({ name: "brigade.openResult", arguments: { revision: 1, activate: true } }); });
  expect(view.getAllByRole("button", { name: "Tests" })).toHaveLength(1);
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  const terminal = view.getByTestId("terminal");
  expect(mocks.mounted).toHaveBeenCalledOnce();
  fireEvent.click(view.getByRole("button", { name: "Приложение" }));
  await act(async () => { await bridge.oncalltool({ name: "brigade.openResult", arguments: { revision: 2, activate: false } }); });
  expect(view.getByRole("button", { name: "Приложение" }).getAttribute("aria-pressed")).toBe("true");
  expect(view.getAllByRole("button", { name: "Tests" })).toHaveLength(1);
  expect(view.getByTestId("terminal")).toBe(terminal);
  expect(mocks.mounted).toHaveBeenCalledOnce();
  expect(mocks.unmounted).not.toHaveBeenCalled();
  fireEvent.click(view.getByRole("button", { name: "Закрыть Tests" }));
  expect(mocks.unmounted).toHaveBeenCalledOnce();
  await act(async () => { await bridge.oncalltool({ name: "brigade.openResult", arguments: { revision: 3, activate: false } }); });
  expect(view.queryByTestId("terminal")).toBeNull();
  expect(view.queryByRole("button", { name: "Tests" })).toBeNull();
});

test.each([0, 7])("command exit %i preserves the named result until manual close", async (exitCode) => {
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const frame = await view.findByTitle("coder.open");
  await waitFor(() => expect(frame.getAttribute("srcdoc")).toContain("<body>App</body>"));
  await waitFor(() => expect(mocks.bridges).toHaveLength(1));
  await act(async () => { await mocks.bridges[0].oncalltool({ name: "brigade.openResult", arguments: { revision: 1, activate: true } }); });
  fireEvent.click(view.getByRole("button", { name: "Запустить" }));
  const terminal = view.getByTestId("terminal");
  const output = view.getByText("Command output");
  fireEvent.click(view.getByRole("button", { name: `exit ${exitCode}` }));
  expect(view.getByRole("button", { name: "Tests" }).getAttribute("aria-pressed")).toBe("true");
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  expect(view.getByText("Command output")).toBe(output);
  expect(output.closest("[hidden]")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Приложение" }));
  await act(async () => { await mocks.bridges[0].oncalltool({ name: "brigade.openResult", arguments: { revision: 2, activate: false } }); });
  expect(view.getByRole("button", { name: "Приложение" }).getAttribute("aria-pressed")).toBe("true");
  fireEvent.click(view.getByRole("button", { name: "Tests" }));
  expect(view.getByTestId("terminal")).toBe(terminal);
  expect(view.getByText("Command output")).toBe(output);
  expect(output.closest("[hidden]")).toBeNull();
  expect(view.getByRole("status").textContent).toContain(`exit code ${exitCode}`);
  expect(view.queryByRole("button", { name: "Запустить" })).toBeNull();
  expect(mocks.mounted).toHaveBeenCalledOnce();
  expect(mocks.unmounted).not.toHaveBeenCalled();
  fireEvent.click(view.getByRole("button", { name: "Закрыть Tests" }));
  expect(view.queryByTestId("terminal")).toBeNull();
  expect(view.queryByRole("button", { name: "Tests" })).toBeNull();
  expect(mocks.unmounted).toHaveBeenCalledOnce();
});

test("appendPrompt preserves the draft and never sends a user message", async () => {
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const frame = await view.findByTitle("coder.open");
  await waitFor(() => expect(frame.getAttribute("srcdoc")).toContain("<body>App</body>"));
  await waitFor(() => expect(mocks.bridges).toHaveLength(1));
  await act(async () => { await mocks.bridges[0].oncalltool({ name: "brigade.appendPrompt", arguments: { text: "Selected diff" } }); });
  expect(mocks.composer.setText).toHaveBeenCalledWith("Existing draft\n\nSelected diff");
  expect(mocks.thread.append).not.toHaveBeenCalled();
});

test("connects the transport before loading iframe HTML and never reconnects on load", async () => {
  let connected!: () => void;
  mocks.connect.mockReturnValueOnce(new Promise<void>((resolve) => { connected = resolve; }));
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const frame = await view.findByTitle("coder.open");
  await waitFor(() => expect(mocks.connect).toHaveBeenCalledOnce());
  expect(frame.hasAttribute("srcdoc")).toBe(false);
  await act(async () => { connected(); });
  expect(frame.getAttribute("srcdoc")).toContain("<body>App</body>");
  fireEvent.load(frame);
  expect(mocks.connect).toHaveBeenCalledOnce();
});

test("unmount closes a pending bridge and prevents stale iframe navigation", async () => {
  let connected!: () => void;
  mocks.connect.mockReturnValueOnce(new Promise<void>((resolve) => { connected = resolve; }));
  const view = render(<McpAppFrame sessionId="s" plugin={plugin} />);
  const frame = await view.findByTitle("coder.open");
  await waitFor(() => expect(mocks.connect).toHaveBeenCalledOnce());
  view.unmount();
  await act(async () => { connected(); });
  expect(frame.hasAttribute("srcdoc")).toBe(false);
  expect(mocks.bridges[0].close).toHaveBeenCalledOnce();
});
