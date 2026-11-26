// @vitest-environment happy-dom
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { SessionDock } from "./SessionDock";

const { sessionGet } = vi.hoisted(() => ({ sessionGet: vi.fn() }));
vi.mock("@/api/client", () => ({ sessionClient: { get: sessionGet } }));
vi.mock("@assistant-ui/react", () => ({ useAuiState: () => [] }));
vi.mock("@/features/sessions/PreviewLinks", () => ({ usePreviews: () => [] }));
vi.mock("@/features/desktop/SessionDesktopTools", () => ({ SessionDesktopTools: () => null }));
vi.mock("./MsgNav", () => ({ MsgNav: () => null }));
vi.mock("./LinksWindow", () => ({ LinksWindow: () => null }));
vi.mock("./McpWindow", () => ({ McpWindow: () => null }));
vi.mock("./TerminalWindow", () => ({ TerminalWindow: ({ onConnChange }: { onConnChange: (value: string) => void }) => <>
  <button onClick={() => onConnChange("open")}>connected</button>
  <button onClick={() => onConnChange("error")}>failed</button>
</> }));
afterEach(() => { cleanup(); localStorage.clear(); vi.clearAllMocks(); });

test("flow toolbar uses an opaque background and bottom border without glass", () => {
  const view = render(<TooltipProvider><SessionDock sessionId="s" placement="flow" readonly /></TooltipProvider>);
  const toolbar = view.getByLabelText("Инструменты сессии");
  expect(toolbar.classList.contains("border-b")).toBe(true);
  expect(toolbar.classList.contains("bg-background")).toBe(true);
  expect(toolbar.className).not.toMatch(/backdrop|after:|bg-background\//);
});

test("hides zero badges and shows positive counts", async () => {
  sessionGet.mockResolvedValue({ session: { mcpServerIds: ["server"] } });
  const view = render(<TooltipProvider><SessionDock sessionId="s" /></TooltipProvider>);
  expect(view.getByRole("button", { name: "Ссылки" }).textContent).toBe("Ссылки");
  await waitFor(() => expect(view.getByRole("button", { name: "MCP" }).textContent).toBe("MCP1"));
});

test("terminal indicator follows the connection, not merely the open window", async () => {
  sessionGet.mockResolvedValue({ session: { mcpServerIds: [] } });
  const view = render(<TooltipProvider><SessionDock sessionId="s" /></TooltipProvider>);
  const terminal = view.getByRole("button", { name: "Терминал" });
  expect(terminal.querySelector("span.bg-muted-foreground")).toBeTruthy();
  fireEvent.click(terminal);
  expect(terminal.querySelector("span.bg-warning")).toBeTruthy();
  fireEvent.click(view.getByText("connected"));
  expect(terminal.querySelector("span.bg-success")).toBeTruthy();
  fireEvent.click(view.getByText("failed"));
  expect(terminal.querySelector("span.bg-destructive")).toBeTruthy();
  fireEvent.click(terminal);
  expect(terminal.querySelector("span.bg-muted-foreground")).toBeTruthy();
});
