// @vitest-environment happy-dom
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Session } from "@/api/gen/brigade/v1/session_pb";
import { SessionPaneSelect } from "./SessionPaneControl";

const { list, setExperience, error } = vi.hoisted(() => ({ list: vi.fn(), setExperience: vi.fn(), error: vi.fn() }));
vi.mock("@/api/client", () => ({ pluginClient: { list }, sessionClient: { setExperience } }));
vi.mock("sonner", () => ({ toast: { error } }));

beforeEach(() => {
  list.mockResolvedValue({ plugins: [
    { id: "coder", name: "Coder", compatible: true, configured: true },
    { id: "wrong", name: "Wrong target", compatible: false, configured: true },
    { id: "unconfigured", name: "Needs config", compatible: true, configured: false },
  ] });
});
afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("lists plain chat and installed compatible configured applications", async () => {
  const view = render(<SessionPaneSelect session={new Session({ id: "s", experienceId: "coder" })} onChanged={vi.fn()} />);
  await waitFor(() => expect(view.getByRole("button").textContent).toContain("Coder"));
  fireEvent.keyDown(view.getByRole("button"), { key: "Enter" });
  expect(await view.findByRole("menuitemradio", { name: "Без панели" })).toBeTruthy();
  expect(view.getByRole("menuitemradio", { name: "Coder" }).getAttribute("aria-checked")).toBe("true");
  expect(view.queryByText("Wrong target")).toBeNull();
  expect(view.queryByText("Needs config")).toBeNull();
});

test("keeps the selected pane and shows local pending until server confirms", async () => {
  let resolve!: (value: { session: Session }) => void;
  setExperience.mockReturnValue(new Promise((done) => { resolve = done; }));
  const onChanged = vi.fn();
  const view = render(<SessionPaneSelect session={new Session({ id: "s", experienceId: "coder" })} name="Coder" onChanged={onChanged} />);
  fireEvent.keyDown(view.getByRole("button"), { key: "Enter" });
  fireEvent.click(await view.findByRole("menuitemradio", { name: "Без панели" }));
  expect(setExperience).toHaveBeenCalledWith({ sessionId: "s", experienceId: "" }, { signal: expect.any(AbortSignal) });
  expect(view.getByRole("button").getAttribute("aria-busy")).toBe("true");
  expect(view.getByRole("button").textContent).toContain("Coder");
  expect(onChanged).not.toHaveBeenCalled();
  const session = new Session({ id: "s", experienceId: "" });
  await act(async () => { resolve({ session }); });
  expect(onChanged).toHaveBeenCalledWith(session);
  expect(view.getByRole("button").getAttribute("aria-busy")).toBe("false");
});

test("failed switch preserves selection and reports the server error", async () => {
  setExperience.mockRejectedValue(new Error("Agent is busy"));
  const onChanged = vi.fn();
  const view = render(<SessionPaneSelect session={new Session({ id: "s", experienceId: "coder" })} name="Coder" onChanged={onChanged} />);
  fireEvent.keyDown(view.getByRole("button"), { key: "Enter" });
  fireEvent.click(await view.findByRole("menuitemradio", { name: "Без панели" }));
  await waitFor(() => expect(error).toHaveBeenCalledWith("Agent is busy"));
  expect(onChanged).not.toHaveBeenCalled();
  expect(view.getByRole("button").getAttribute("aria-busy")).toBe("false");
});

test("unmount aborts an in-flight switch and ignores its late result", async () => {
  let resolve!: (value: { session: Session }) => void;
  setExperience.mockReturnValue(new Promise((done) => { resolve = done; }));
  const onChanged = vi.fn();
  const view = render(<SessionPaneSelect session={new Session({ id: "s", experienceId: "coder" })} name="Coder" onChanged={onChanged} />);
  fireEvent.keyDown(view.getByRole("button"), { key: "Enter" });
  fireEvent.click(await view.findByRole("menuitemradio", { name: "Без панели" }));
  const signal = setExperience.mock.calls[0][1].signal as AbortSignal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => { resolve({ session: new Session({ id: "s" }) }); });
  expect(onChanged).not.toHaveBeenCalled();
});

test("does not allow switching while the ACP runtime is busy", async () => {
  const view = render(<SessionPaneSelect session={new Session({ id: "s", experienceId: "coder" })} name="Coder" onChanged={vi.fn()} disabled />);
  fireEvent.keyDown(view.getByRole("button"), { key: "Enter" });
  const plain = await view.findByRole("menuitemradio", { name: "Без панели" });
  expect(plain.getAttribute("aria-disabled")).toBe("true");
  fireEvent.click(plain);
  expect(setExperience).not.toHaveBeenCalled();
});
