// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { useEffect, type ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { SessionWorkspace } from "./SessionWorkspace";

const { get, mounted, frameRender } = vi.hoisted(() => ({ get: vi.fn(), mounted: vi.fn(), frameRender: vi.fn() }));
vi.mock("@/api/client", () => ({ pluginClient: { get } }));
vi.mock("@/features/acp/AcpPage", () => ({
  AcpSession: ({ pane, controls }: { pane?: ReactNode; controls?: ReactNode }) => {
    useEffect(() => { mounted(); }, []);
    return <div data-testid="runtime">{controls}<div data-testid="chat" />{pane}</div>;
  },
}));
vi.mock("./SessionPaneControl", () => ({
  SessionPaneControl: ({ session, onChanged }: { session: Session; onChanged: (session: Session) => void }) => <>
    <button onClick={() => onChanged(new Session({ ...session, experienceId: "" }))}>plain</button>
    <button onClick={() => onChanged(new Session({ ...session, experienceId: "coder" }))}>coder</button>
  </>,
}));
vi.mock("@/features/plugins/McpAppFrame", () => ({ McpAppFrame: ({ plugin }: { plugin: { entryTool: string } }) => { frameRender(); return <div data-testid="app">{plugin.entryTool}</div>; } }));

beforeEach(() => { get.mockResolvedValue({ name: "CAD", entryTool: "cad.open", layout: "" }); });
afterEach(() => { cleanup(); vi.resetAllMocks(); });

test.each(["", "full", "split"])("legacy layout %s cannot replace the chat", async (layout) => {
  get.mockResolvedValue({ name: "CAD", entryTool: "cad.open", layout });
  const view = render(<SessionWorkspace session={new Session({ id: "session", experienceId: "cad" })} />);
  expect(view.getByTestId("chat")).toBeTruthy();
  await waitFor(() => expect(view.getByTestId("app")).toBeTruthy());
  expect(view.getByTestId("chat")).toBeTruthy();
});

test.each(["coder", "cad"])("%s split uses the native chat and app in the same runtime", async (id) => {
  get.mockResolvedValue({ name: id, entryTool: `${id}.open`, layout: "split" });
  const view = render(<SessionWorkspace session={new Session({ id: "session", experienceId: id })} />);
  await view.findByText(`${id}.open`);
  expect(view.getByTestId("chat")).toBeTruthy();
  expect(mounted).toHaveBeenCalledTimes(1);
});

test("switching app to plain and back preserves the runtime and ignores late metadata", async () => {
  let resolve!: (plugin: { name: string; entryTool: string; layout: string }) => void;
  get.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  const view = render(<SessionWorkspace session={new Session({ id: "session", experienceId: "cad" })} />);
  fireEvent.click(view.getByText("plain"));
  expect(view.getByTestId("chat")).toBeTruthy();
  await act(async () => { resolve({ name: "CAD", entryTool: "cad.open", layout: "full" }); });
  expect(view.queryByTestId("app")).toBeNull();
  get.mockResolvedValue({ name: "Coder", entryTool: "coder.open", layout: "split" });
  fireEvent.click(view.getByText("coder"));
  await view.findByText("coder.open");
  expect(view.getByTestId("chat")).toBeTruthy();
  expect(mounted).toHaveBeenCalledTimes(1);
});

test("plain reload uses persisted empty experience without fetching a plugin", () => {
  const view = render(<SessionWorkspace session={new Session({ id: "session" })} />);
  expect(view.getByTestId("chat")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

test("metadata failure leaves the selector available to return to plain chat", async () => {
  get.mockRejectedValue(new Error("Plugin unavailable"));
  const view = render(<SessionWorkspace session={new Session({ id: "session", experienceId: "cad" })} />);
  expect(await view.findByRole("alert")).toBeTruthy();
  expect(view.getByTestId("chat")).toBeTruthy();
  fireEvent.click(view.getByText("plain"));
  expect(view.getByTestId("chat")).toBeTruthy();
  expect(mounted).toHaveBeenCalledTimes(1);
});

test("pane rendering errors cannot unmount the chat runtime", async () => {
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  frameRender.mockImplementation(() => { throw new Error("Pane render failed"); });
  try {
    const view = render(<SessionWorkspace session={new Session({ id: "session", experienceId: "cad" })} />);
    expect(await view.findByRole("alert")).toBeTruthy();
    expect(view.getByText(/Pane render failed/)).toBeTruthy();
    expect(view.getByTestId("chat")).toBeTruthy();
    frameRender.mockImplementation(() => {});
    fireEvent.click(view.getByText("Повторить"));
    await view.findByTestId("app");
    expect(mounted).toHaveBeenCalledOnce();
  } finally { consoleError.mockRestore(); }
});
