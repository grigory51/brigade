// @vitest-environment happy-dom
import { useEffect, type ReactNode } from "react";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { AcpSession } from "@/features/acp/AcpPage";

const { mounted, resolvePermission } = vi.hoisted(() => ({ mounted: vi.fn(), resolvePermission: vi.fn() }));
vi.mock("@/api/client", () => ({
  responseProfileClient: { list: async () => ({ profiles: [] }) },
  sessionClient: { get: async () => ({ session: { responseProfileId: "default" } }) },
}));
vi.mock("@assistant-ui/react", () => ({
  AssistantRuntimeProvider: ({ children }: { children: ReactNode }) => children,
  useAui: () => ({ thread: {} }),
  useAuiState: (select: (state: { thread: { isRunning: boolean; isLoading: boolean; messages: [] }; composer: { text: string } }) => unknown) => select({ thread: { isRunning: false, isLoading: false, messages: [] }, composer: { text: "" } }),
}));
vi.mock("@/components/assistant-ui/composer-context", () => ({ PendingContextProvider: ({ children }: { children: ReactNode }) => children }));
vi.mock("@/features/acp/SelectionMenu", () => ({ SelectionMenu: () => null }));
vi.mock("@/features/acp/dock/SessionDock", () => ({
  SessionDock: ({ controls }: { controls?: ReactNode }) => <div aria-label="Инструменты сессии">{controls}</div>,
}));
vi.mock("@/features/acp/useAcpRuntime", () => ({
  useAcpRuntime: () => {
    useEffect(() => { mounted(); }, []);
    return {
      runtime: {}, permission: { id: "permission" }, resolvePermission,
      commands: [], plan: [], configOptions: [], workflows: [], status: { tick: 0, seq: 0, generating: false },
    };
  },
}));
vi.mock("@/features/acp/AcpThread", () => ({
  AcpThread: ({ onPermissionDecision }: { onPermissionDecision: (decision: string) => void }) => <div><textarea aria-label="Чат" /><button onClick={() => onPermissionDecision("allow")}>Разрешить</button></div>,
  PermissionComposer: ({ onDecide }: { onDecide: (decision: string) => void }) => <button onClick={() => onDecide("allow")}>Разрешить</button>,
}));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

test.each([false, true])("pane %s retains the shared session controls inside the runtime", (withPane) => {
  const view = render(<AcpSession sessionId="s" pane={withPane ? <div>App</div> : undefined} controls={<button>Интерфейс сессии</button>} />);
  expect(view.getByRole("button", { name: "Интерфейс сессии" }).closest('[aria-label="Инструменты сессии"]')).toBeTruthy();
  expect(mounted).toHaveBeenCalledOnce();
});

test("split places native chat before app and uses only its permission composer", () => {
  const view = render(<AcpSession sessionId="s" pane={<div>App</div>} />);
  const chat = view.getByRole("textbox", { name: "Чат" });
  const panel = view.getByRole("complementary", { name: "Панель сессии" });
  expect(chat.compareDocumentPosition(panel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(view.getAllByRole("button", { name: "Разрешить" })).toHaveLength(1);
  fireEvent.click(view.getByRole("button", { name: "Разрешить" }));
  expect(resolvePermission).toHaveBeenCalledWith("permission", "allow");
  expect(mounted).toHaveBeenCalledOnce();
});

test("switching plain to split retains native chat DOM and runtime", () => {
  const view = render(<AcpSession sessionId="s" />);
  const chat = view.getByRole("textbox", { name: "Чат" });
  fireEvent.change(chat, { target: { value: "draft" } });
  view.rerender(<AcpSession sessionId="s" pane={<div>App</div>} />);
  expect(view.getByRole("textbox", { name: "Чат" })).toBe(chat);
  expect((chat as HTMLTextAreaElement).value).toBe("draft");
  expect(mounted).toHaveBeenCalledOnce();
});

test("switching panes and closing the pane never remounts the chat", () => {
  const view = render(<AcpSession sessionId="s" pane={<div>CAD</div>} />);
  const chat = view.getByRole("textbox", { name: "Чат" });
  fireEvent.change(chat, { target: { value: "draft" } });
  view.rerender(<AcpSession sessionId="s" pane={<div>Coder</div>} />);
  expect(view.getByRole("textbox", { name: "Чат" })).toBe(chat);
  expect(view.getByText("Coder")).toBeTruthy();
  view.rerender(<AcpSession sessionId="s" />);
  expect(view.getByRole("textbox", { name: "Чат" })).toBe(chat);
  expect((chat as HTMLTextAreaElement).value).toBe("draft");
  expect(view.queryByRole("complementary")).toBeNull();
  expect(view.getAllByRole("button", { name: "Разрешить" })).toHaveLength(1);
  expect(mounted).toHaveBeenCalledOnce();
});
