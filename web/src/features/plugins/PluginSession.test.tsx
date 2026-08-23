// @vitest-environment happy-dom

import { render, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { expect, test, vi } from "vitest";
import type { Session } from "@/api/gen/brigade/v1/session_pb";
import { PluginSession } from "./PluginSession";

vi.mock("@/api/client", () => ({
  pluginClient: { get: () => Promise.resolve({ name: "CAD", entryTool: "cad.open" }) },
}));
vi.mock("@/features/acp/AcpPage", () => ({
  AcpSession: ({ workspace, experience }: { workspace?: boolean; experience?: ReactNode }) => (
    <div data-testid="runtime" data-workspace={workspace}>{experience}</div>
  ),
}));
vi.mock("@/features/sessions/SessionHeaderSlot", () => ({ useSessionHeader: () => {} }));
vi.mock("./McpAppFrame", () => ({ McpAppFrame: () => <div data-testid="app" /> }));

test("plugin owns the whole session workspace inside the ACP runtime", async () => {
  const view = render(<PluginSession session={{ id: "session", experienceId: "cad" } as Session} />);

  await waitFor(() => expect(view.getByTestId("app")).toBeTruthy());
  expect(view.getByTestId("runtime").dataset.workspace).toBe("true");
  expect(view.queryByTestId("chat")).toBeNull();
});
