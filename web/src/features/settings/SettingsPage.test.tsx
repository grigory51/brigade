// @vitest-environment happy-dom
import { cleanup, fireEvent, render, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { SettingsPage } from "./SettingsPage";

vi.mock("@/features/auth/AuthContext", () => ({ useAuth: () => ({ desktop: false }) }));
vi.mock("@/api/client", () => ({
  agentClient: { listConnections: async () => ({ connections: [] }) },
  authClient: {
    getMemorySettings: async () => ({ remote: "" }),
    getSSHSettings: async () => ({ publicKey: "" }),
  },
  notificationClient: { listNotificationBackends: async () => ({ backends: [] }) },
  telegramClient: { listBots: async () => ({ bots: [], mode: "poll" }) },
  mcpClient: {
    listServers: async () => ({ servers: [] }),
    listSecrets: async () => ({ secrets: [] }),
  },
  pluginClient: { list: async () => ({ plugins: [], requiredTarget: "linux-amd64" }) },
}));

afterEach(cleanup);

function Location() {
  return <output aria-label="Маршрут">{useLocation().pathname}</output>;
}

test.each(["/settings/mcp", "/settings/apps"])("%s opens unified MCP settings", async (path) => {
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <Location />
      <Routes><Route path="/settings/:section" element={<SettingsPage />} /></Routes>
    </MemoryRouter>,
  );
  expect(await view.findByRole("heading", { name: "MCP Apps", level: 3 })).toBeTruthy();
  expect(await view.findByRole("heading", { name: "MCP-серверы", level: 3 })).toBeTruthy();
  expect(view.getByRole("heading", { name: "MCP", level: 2 })).toBeTruthy();
  const navigation = within(view.getByRole("navigation"));
  expect(navigation.getByRole("button", { name: "MCP" })).toBeTruthy();
  expect(navigation.queryByRole("button", { name: "MCP Apps" })).toBeNull();
  expect(view.getByLabelText("Маршрут").textContent).toBe("/settings/mcp");
  fireEvent.click(view.getByRole("button", { name: "Добавить сервер" }));
  expect(view.getByRole("button", { name: "Загрузить .mcpb" })).toBeTruthy();
});
