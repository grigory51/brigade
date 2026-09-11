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

test("settings navigation closes after selecting a section or pressing Escape", async () => {
  const view = render(
    <MemoryRouter initialEntries={["/settings/mcp"]}>
      <Location />
      <Routes><Route path="/settings/:section" element={<SettingsPage />} /></Routes>
    </MemoryRouter>,
  );
  await view.findByRole("heading", { name: "MCP Apps", level: 3 });
  const trigger = view.getByRole("button", { name: "Разделы" });
  const navigation = view.getByRole("navigation", { name: "Разделы настроек" });
  expect(trigger.getAttribute("aria-controls")).toBe(navigation.id);
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  fireEvent.click(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  fireEvent.click(within(navigation).getByRole("button", { name: "Общее" }));
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(view.getByLabelText("Маршрут").textContent).toBe("/settings/general");
  expect(document.activeElement).toBe(trigger);
  fireEvent.click(trigger);
  fireEvent.keyDown(navigation, { key: "Escape" });
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger);
});

test("opening settings navigation preserves the current form draft", async () => {
  const view = render(
    <MemoryRouter initialEntries={["/settings/mcp"]}>
      <Routes><Route path="/settings/:section" element={<SettingsPage />} /></Routes>
    </MemoryRouter>,
  );
  await view.findByRole("heading", { name: "MCP Apps", level: 3 });
  fireEvent.click(view.getByRole("button", { name: "Добавить сервер" }));
  const inputs = view.getAllByRole("textbox");
  const input = inputs[0] as HTMLInputElement;
  fireEvent.change(input, { target: { value: "draft-value" } });
  const trigger = view.getByRole("button", { name: "Разделы" });
  fireEvent.click(trigger);
  fireEvent.click(trigger);
  expect(input.isConnected).toBe(true);
  expect(input.value).toBe("draft-value");
});
