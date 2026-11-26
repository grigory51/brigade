import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { callPluginHostTool as callHostTool } from "./host-tools";

function callPluginHostTool(sessionId: string, params: Parameters<typeof callHostTool>[1], onOpen: Parameters<typeof callHostTool>[2], signal: AbortSignal) {
  return callHostTool(sessionId, params, onOpen, signal, ["coder.state"], { id: "coder", version: "1" });
}

const { getCI, mCP, listPreviews, list, stat, read } = vi.hoisted(() => ({ getCI: vi.fn(), mCP: vi.fn(), listPreviews: vi.fn(), list: vi.fn(), stat: vi.fn(), read: vi.fn() }));
vi.mock("@/api/client", () => ({ codingClient: { getCI }, pluginClient: { mCP }, sessionClient: { listPreviews }, workspaceClient: { list, stat, read } }));

const webResult = { name: "App", kind: "web", url: "https://preview.test/project/page" };
function state(result: unknown = webResult, revision = 3) {
  return { resultJson: new TextEncoder().encode(JSON.stringify({ structuredContent: { schemaVersion: 1, revision, results: [result] } })) };
}
const open = { name: "brigade.openResult", arguments: { stateTool: "coder.state", name: "App", revision: 3 } };
beforeEach(() => {
  mCP.mockResolvedValue(state());
  listPreviews.mockResolvedValue({ previews: [{ port: 3000, url: "https://preview.test/project/" }] });
});
afterEach(() => { vi.resetAllMocks(); });

test("CI uses the host session and forwards only projectPath", async () => {
  getCI.mockResolvedValue({ toJson: () => ({ runs: [], partial: true, warnings: ["Unavailable"] }) });
  const signal = new AbortController().signal;
  const result = await callPluginHostTool("session", { name: "brigade.coding.getCI", arguments: { projectPath: "app" } }, vi.fn(), signal);
  expect(getCI).toHaveBeenCalledWith({ sessionId: "session", projectPath: "app" }, { signal });
  expect(result?.structuredContent).toEqual({ runs: [], partial: true, warnings: ["Unavailable"] });
  expect((await callPluginHostTool("session", { name: "brigade.coding.getCI", arguments: { sessionId: "other" } }, vi.fn(), signal))?.isError).toBe(true);
  expect(getCI).toHaveBeenCalledTimes(1);
});

test("CI errors retain structured reasons and retry metadata for the app", async () => {
  getCI.mockRejectedValue(new ConnectError("CI rate limit", Code.ResourceExhausted, { "coding-reason": "rate-limited", "retry-after": "15" }));
  const result = await callPluginHostTool("session", { name: "brigade.coding.getCI", arguments: {} }, vi.fn(), new AbortController().signal);
  expect(result?.isError).toBe(true);
  expect(result?.structuredContent).toEqual({ error: { code: Code.ResourceExhausted, message: "CI rate limit", reason: "rate-limited", retryAfterSeconds: 15 } });
});

test("file browsing uses the existing workspace API and binds the host session", async () => {
  list.mockResolvedValue({ toJson: () => ({ entries: [{ name: "README.md", path: "app/README.md" }] }) });
  stat.mockResolvedValue({ entry: { directory: false, size: 5n } });
  read.mockResolvedValue({ content: new TextEncoder().encode("hello") });
  const signal = new AbortController().signal;
  const result = await callPluginHostTool("session", { name: "brigade.workspace.list", arguments: { path: "app" } }, vi.fn(), signal);
  expect(result?.structuredContent?.entries).toHaveLength(1);
  expect(list).toHaveBeenCalledWith({ sessionId: "session", path: "app", pageSize: 500, pageToken: "" }, { signal });
  expect((await callPluginHostTool("session", { name: "brigade.workspace.read", arguments: { path: "app/README.md" } }, vi.fn(), signal))?.structuredContent).toEqual({ text: "hello", binary: false, truncated: false });
  expect(read).toHaveBeenCalledWith({ sessionId: "session", path: "app/README.md", offset: 0n, size: 1 << 20 }, { signal });
});

test("file listing forwards a bounded continuation page", async () => {
  list.mockResolvedValue({ toJson: () => ({ entries: [], nextPageToken: "1000" }) });
  const signal = new AbortController().signal;
  const result = await callPluginHostTool("session", { name: "brigade.workspace.list", arguments: { path: ".", pageToken: "500" } }, vi.fn(), signal);
  expect(list).toHaveBeenCalledWith({ sessionId: "session", path: ".", pageSize: 500, pageToken: "500" }, { signal });
  expect(result?.structuredContent?.nextPageToken).toBe("1000");
});

test.each(["/etc/passwd", "../secret", "app/../../secret", "app\\secret", "a\0b"])("rejects paths outside workspace: %s", async (path) => {
  const result = await callPluginHostTool("session", { name: "brigade.workspace.read", arguments: { path } }, vi.fn(), new AbortController().signal);
  expect(result?.isError).toBe(true);
  expect(stat).not.toHaveBeenCalled();
});

test("rejects foreign session parameters and exposes no workspace mutations", async () => {
  const signal = new AbortController().signal;
  expect((await callPluginHostTool("session", { name: "brigade.workspace.list", arguments: { path: ".", sessionId: "other" } }, vi.fn(), signal))?.isError).toBe(true);
  expect(await callPluginHostTool("session", { name: "brigade.workspace.write", arguments: { path: "README.md" } }, vi.fn(), signal)).toBeUndefined();
  expect(list).not.toHaveBeenCalled();
});

test("binary files and a truncated UTF-8 boundary are handled without replacement characters", async () => {
  const signal = new AbortController().signal;
  stat.mockResolvedValue({ entry: { directory: false, size: 2n } });
  read.mockResolvedValueOnce({ content: new Uint8Array([0, 255]) });
  expect((await callPluginHostTool("session", { name: "brigade.workspace.read", arguments: { path: "a" } }, vi.fn(), signal))?.structuredContent?.binary).toBe(true);
  stat.mockResolvedValue({ entry: { directory: false, size: 2000000n } });
  read.mockResolvedValueOnce({ content: new Uint8Array([65, 0xd0]) });
  expect((await callPluginHostTool("session", { name: "brigade.workspace.read", arguments: { path: "b" } }, vi.fn(), signal))?.structuredContent).toEqual({ text: "A", binary: false, truncated: true });
});

test("opens only a matching registered preview with stable identity and requested activation", async () => {
  const onOpen = vi.fn();
  const signal = new AbortController().signal;
  await callPluginHostTool("session", { ...open, arguments: { ...open.arguments, activate: false } }, onOpen, signal);
  expect(onOpen).toHaveBeenCalledWith({ key: JSON.stringify(["coder.state", "App"]), revision: 3, result: webResult }, false);
  expect(listPreviews).toHaveBeenCalledWith({ sessionId: "session" }, { signal });
  const request = mCP.mock.calls[0][0];
  expect(request.sessionId).toBe("session");
  expect(request.expectedPlugin).toEqual({ id: "coder", version: "1" });
  expect(JSON.parse(new TextDecoder().decode(request.paramsJson))).toEqual({ name: "coder.state", arguments: {} });
});

test.each([
  "https://other.test/project/page",
  "https://preview.test:444/project/page",
  "https://preview.test/project-other/page",
  "https://preview.test/project/../secret",
  "https://user:password@preview.test/project/page",
  "javascript:alert(1)",
])("rejects an unregistered or unsafe preview: %s", async (url) => {
  mCP.mockResolvedValue(state({ ...webResult, url }));
  const onOpen = vi.fn();
  const result = await callPluginHostTool("session", open, onOpen, new AbortController().signal);
  expect(result?.isError).toBe(true);
  expect(onOpen).not.toHaveBeenCalled();
});

test("rejects stale revisions instead of opening a different command", async () => {
  mCP.mockResolvedValue(state({ name: "App", kind: "terminal", command: { argv: ["npm", "run", "dev"], cwd: "." } }, 4));
  const onOpen = vi.fn();
  expect((await callPluginHostTool("session", open, onOpen, new AbortController().signal))?.isError).toBe(true);
  expect(onOpen).not.toHaveBeenCalled();
});

test.each(["other-3000", "session-3000&id=other-3000"])("checks the registered routing query: %s", async (id) => {
  listPreviews.mockResolvedValue({ previews: [{ url: "https://preview.test/?id=session-3000" }] });
  mCP.mockResolvedValue(state({ ...webResult, url: `https://preview.test/?id=${id}` }));
  const onOpen = vi.fn();
  expect((await callPluginHostTool("session", open, onOpen, new AbortController().signal))?.isError).toBe(true);
  expect(onOpen).not.toHaveBeenCalled();
});

test("passes a registered command to host confirmation without executing it", async () => {
  const result = { name: "App", kind: "terminal", command: { argv: ["npm", "run", "dev"], cwd: "app" } };
  mCP.mockResolvedValue(state(result));
  const onOpen = vi.fn();
  await callPluginHostTool("session", open, onOpen, new AbortController().signal);
  expect(onOpen).toHaveBeenCalledWith({ key: JSON.stringify(["coder.state", "App"]), revision: 3, result }, undefined);
  expect(listPreviews).not.toHaveBeenCalled();
});

test("cancelled request cannot open a result", async () => {
  const controller = new AbortController();
  controller.abort();
  const onOpen = vi.fn();
  expect((await callPluginHostTool("session", open, onOpen, controller.signal))?.isError).toBe(true);
  expect(onOpen).not.toHaveBeenCalled();
});

test("rejects undeclared or mutating state tools before any MCP invocation", async () => {
  const onOpen = vi.fn();
  const result = await callHostTool("session", { ...open, arguments: { ...open.arguments, stateTool: "coder.register_result" } }, onOpen, new AbortController().signal, ["coder.state"], { id: "coder", version: "1" });
  expect(result?.isError).toBe(true);
  expect(mCP).not.toHaveBeenCalled();
  expect(onOpen).not.toHaveBeenCalled();
});
