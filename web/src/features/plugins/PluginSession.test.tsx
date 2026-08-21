// @vitest-environment happy-dom

import { render, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { PluginSession } from "./PluginSession";

vi.mock("@/api/client", () => ({
  pluginClient: { get: () => Promise.resolve({ name: "CAD", entryTool: "cad.open" }) },
}));
vi.mock("@/features/acp/AcpPage", () => ({
  AcpSession: ({ workspace }: { workspace?: boolean }) => <div data-testid="chat" data-workspace={workspace} />,
}));
vi.mock("@/features/sessions/SessionHeaderSlot", () => ({ useSessionHeader: () => {} }));
vi.mock("./McpAppFrame", () => ({ McpAppFrame: () => <div data-testid="app" /> }));

test("plugin workspace keeps app and compact chat visible together", async () => {
  const view = render(<PluginSession session={{ id: "session", experienceId: "cad" } as Session} />);

  await waitFor(() => expect(view.getByTestId("app")).toBeTruthy());
  expect(view.getByTestId("chat").dataset.workspace).toBe("true");
  expect(view.queryByRole("tab")).toBeNull();
});
