// @vitest-environment happy-dom

import { fireEvent, render, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { PluginSection } from "./PluginSection";

vi.mock("@/api/client", () => ({
  pluginClient: {
    list: () => Promise.resolve({
      requiredTarget: "linux-amd64",
      plugins: [{
        id: "cad",
        name: "CAD",
        description: "Parametric CAD",
        version: "1.0.0",
        system: true,
        compatible: true,
        variants: [{ version: "1.0.0", target: "linux-amd64", source: "https://example.test/cad.mcpb" }],
        configSchemaJson: new TextEncoder().encode("null"),
        configValuesJson: new TextEncoder().encode("null"),
        configuredSecrets: [],
      }],
    }),
  },
  refreshSession: vi.fn(),
}));

test("system app URL can be opened for a personal override", async () => {
  const view = render(<PluginSection />);
  const card = await view.findByRole("button", { name: /CAD/ });

  fireEvent.click(card);

  await waitFor(() => expect(view.getAllByPlaceholderText("https://…/application.mcpb")).toHaveLength(2));
  expect((view.getAllByPlaceholderText("https://…/application.mcpb")[1] as HTMLInputElement).value)
    .toBe("https://example.test/cad.mcpb");
});
