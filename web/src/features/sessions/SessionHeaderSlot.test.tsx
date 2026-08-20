// @vitest-environment happy-dom

import { render, waitFor } from "@testing-library/react";
import { expect, test } from "vitest";
import {
  SessionHeaderProvider,
  useSessionHeader,
  useSessionHeaderSlot,
} from "./SessionHeaderSlot";

function Publisher() {
  useSessionHeader({ title: "CAD" });
  return null;
}

function Header() {
  const { title } = useSessionHeaderSlot();
  return <header>{title}</header>;
}

test("session header update settles after publishing", async () => {
  const view = render(
    <SessionHeaderProvider>
      <Publisher />
      <Header />
    </SessionHeaderProvider>,
  );

  await waitFor(() => expect(view.getByText("CAD")).toBeTruthy());
});
