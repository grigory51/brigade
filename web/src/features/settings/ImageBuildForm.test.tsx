// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { authClient } from "@/api/client";
import { AgentImageBuild, AgentImagesSettings, AgentRuntimeSettings } from "@/api/gen/brigade/v1/auth_pb";
import { EnvironmentSection } from "./EnvironmentSection";
import { ImageBuildForm } from "./ImageBuildForm";

vi.mock("@/api/client", () => ({
  authClient: {
    getAgentImageBuild: vi.fn(),
    startAgentImageBuild: vi.fn(),
    cancelAgentImageBuild: vi.fn(),
    getAgentImages: vi.fn(),
    getAgentRuntime: vi.fn(),
    setAgentImages: vi.fn(),
    setAgentRuntime: vi.fn(),
  },
}));

test.each(["docker", "local"])("server mode %s is shown once without redundant description", async (mode) => {
  vi.mocked(authClient.getAgentRuntime).mockResolvedValue(new AgentRuntimeSettings({ mode, runningMode: mode, editable: false }));
  vi.mocked(authClient.getAgentImages).mockResolvedValue(new AgentImagesSettings());
  const view = render(<EnvironmentSection />);
  await act(async () => {});
  expect(view.getAllByText(mode === "docker" ? "Docker" : "Local")).toHaveLength(1);
  expect(view.queryByText("Режим")).toBeNull();
  expect(view.queryByText(/Где исполняются сессии/)).toBeNull();
  expect(view.getByText(/задан администратором/)).toBeTruthy();
});

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.mocked(authClient.getAgentImageBuild).mockResolvedValue(new AgentImageBuild());
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

async function mountForm() {
  const onSucceeded = vi.fn().mockResolvedValue(undefined);
  const onBusyChange = vi.fn();
  const view = render(<ImageBuildForm disabled={false} onSucceeded={onSucceeded} onBusyChange={onBusyChange} />);
  await act(async () => {});
  return { ...view, onSucceeded, onBusyChange };
}

test("restores a build and polls sequentially until success", async () => {
  let resolvePoll!: (build: AgentImageBuild) => void;
  const building = new AgentImageBuild({ id: "build-1", name: "python-tools", script: "echo saved", status: "building", log: "installing" });
  vi.mocked(authClient.getAgentImageBuild)
    .mockResolvedValueOnce(building)
    .mockImplementationOnce(() => new Promise((resolve) => { resolvePoll = resolve; }));
  const view = await mountForm();
  const editor = view.getByLabelText("Скрипт установки") as HTMLTextAreaElement;
  const name = view.getByLabelText("Название") as HTMLInputElement;
  expect(authClient.getAgentImageBuild).toHaveBeenCalledWith({}, { signal: expect.any(AbortSignal), timeoutMs: 15_000 });
  expect(view.getByText(/Скрипт выполняется от root при сборке\. Не добавляйте токены и пароли: скрипт и лог сохраняются\./)).toBeTruthy();
  expect(editor.value).toBe("echo saved");
  expect(editor.readOnly).toBe(true);
  expect(name.value).toBe("python-tools");
  expect(name.disabled).toBe(true);
  expect(view.onBusyChange).toHaveBeenLastCalledWith(true);
  expect(view.getByText("installing").closest("[aria-live]")).toBeNull();

  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(authClient.getAgentImageBuild).toHaveBeenCalledTimes(2);
  expect(authClient.getAgentImageBuild).toHaveBeenLastCalledWith({}, { signal: expect.any(AbortSignal), timeoutMs: 15_000 });
  await act(async () => { resolvePoll(new AgentImageBuild({ ...building, status: "succeeded", image: "agent:built" })); });
  expect(view.onSucceeded).toHaveBeenCalledTimes(1);
  expect(view.onBusyChange).toHaveBeenLastCalledWith(false);
  expect(view.getByRole("status").textContent).toBe("Образ собран");
  expect(view.getByText("python-tools")).toBeTruthy();
  expect(view.queryByText("agent:built")).toBeNull();
  expect(name.disabled).toBe(false);
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(authClient.getAgentImageBuild).toHaveBeenCalledTimes(2);
});

test("shows initial errors and retries explicitly", async () => {
  vi.mocked(authClient.getAgentImageBuild).mockRejectedValueOnce(new Error("offline"));
  const view = await mountForm();
  expect(view.getByRole("alert").textContent).toContain("Не удалось получить состояние");
  expect((view.getByRole("button", { name: "Собрать" }) as HTMLButtonElement).disabled).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Повторить запрос" })); });
  expect(authClient.getAgentImageBuild).toHaveBeenCalledTimes(2);
  expect(view.queryByRole("alert")).toBeNull();
  expect(view.onBusyChange).toHaveBeenLastCalledWith(false);
});

test("name is required for both build actions and is disabled while submitting", async () => {
  let resolveStart!: (build: AgentImageBuild) => void;
  vi.mocked(authClient.startAgentImageBuild).mockImplementationOnce(() => new Promise((resolve) => { resolveStart = resolve; }));
  const view = await mountForm();
  const name = view.getByLabelText("Название") as HTMLInputElement;
  const build = view.getByRole("button", { name: "Собрать" }) as HTMLButtonElement;
  const rebuild = view.getByRole("button", { name: "Пересобрать без кеша" }) as HTMLButtonElement;
  expect(name.required).toBe(true);
  expect(name.maxLength).toBe(40);
  expect(name.placeholder).toBe("python-tools");
  fireEvent.change(view.getByLabelText("Скрипт установки"), { target: { value: "echo draft" } });
  expect(build.disabled).toBe(true);
  expect(rebuild.disabled).toBe(true);
  fireEvent.change(name, { target: { value: "   " } });
  expect(build.disabled).toBe(true);
  expect(rebuild.disabled).toBe(true);
  fireEvent.change(name, { target: { value: "python-tools" } });
  expect(build.disabled).toBe(false);
  expect(rebuild.disabled).toBe(false);
  await act(async () => { fireEvent.click(build); });
  expect(name.disabled).toBe(true);
  expect(build.disabled).toBe(true);
  expect(rebuild.disabled).toBe(true);
  await act(async () => { resolveStart(new AgentImageBuild({ id: "build-1", name: "python-tools", status: "queued" })); });
});

test("successful legacy builds display their image reference", async () => {
  vi.mocked(authClient.getAgentImageBuild).mockResolvedValue(new AgentImageBuild({ id: "legacy", image: "agent:legacy", status: "succeeded" }));
  const view = await mountForm();
  expect(view.getByText("agent:legacy")).toBeTruthy();
  expect((view.getByLabelText("Название") as HTMLInputElement).value).toBe("");
});

test.each([
  ["Собрать", false],
  ["Пересобрать без кеша", true],
] as const)("%s sends the script and preserves edits across polling", async (button, noCache) => {
  const building = new AgentImageBuild({ id: "build-1", name: "server-tools", script: "server script", status: "queued" });
  vi.mocked(authClient.startAgentImageBuild).mockResolvedValue(building);
  const view = await mountForm();
  const editor = view.getByLabelText("Скрипт установки") as HTMLTextAreaElement;
  const name = view.getByLabelText("Название") as HTMLInputElement;
  fireEvent.change(name, { target: { value: "draft-tools" } });
  fireEvent.change(editor, { target: { value: "echo draft\n" } });
  await act(async () => {
    fireEvent.click(view.getByRole("button", { name: button }));
    fireEvent.click(view.getByRole("button", { name: button }));
  });
  expect(authClient.startAgentImageBuild).toHaveBeenCalledTimes(1);
  expect(authClient.startAgentImageBuild).toHaveBeenCalledWith({ name: "draft-tools", script: "echo draft\n", noCache }, { signal: expect.any(AbortSignal), timeoutMs: 15_000 });
  expect(name.disabled).toBe(true);
  vi.mocked(authClient.getAgentImageBuild).mockResolvedValue(new AgentImageBuild({ ...building, status: "failed", error: "command failed" }));
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(editor.value).toBe("echo draft\n");
  expect(name.value).toBe("draft-tools");
  expect(name.disabled).toBe(false);
  expect(editor.readOnly).toBe(false);
  expect(view.getByText("command failed")).toBeTruthy();
  expect(vi.getTimerCount()).toBe(0);
});

test("cancel aborts a hung poll and sends its RPC immediately without a false error", async () => {
  let resolveCancel!: (build: AgentImageBuild) => void;
  const building = new AgentImageBuild({ id: "build-1", status: "building" });
  vi.mocked(authClient.getAgentImageBuild)
    .mockResolvedValueOnce(building)
    .mockImplementationOnce((_, options) => new Promise((_, reject) => {
      options?.signal?.addEventListener("abort", () => {
        reject(new ConnectError("poll aborted", Code.Canceled));
      }, { once: true });
    }));
  vi.mocked(authClient.cancelAgentImageBuild).mockImplementationOnce(() => new Promise((resolve) => { resolveCancel = resolve; }));
  const view = await mountForm();
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  const beforeCancel = Date.now();
  const initialSignal = vi.mocked(authClient.getAgentImageBuild).mock.calls[0][1]?.signal;
  const pollSignal = vi.mocked(authClient.getAgentImageBuild).mock.calls[1][1]?.signal;
  await act(async () => {
    fireEvent.click(view.getByRole("button", { name: "Отменить" }));
    fireEvent.click(view.getByRole("button", { name: "Отменить" }));
  });
  expect(pollSignal?.aborted).toBe(true);
  expect(initialSignal?.aborted).toBe(false);
  expect(authClient.cancelAgentImageBuild).toHaveBeenCalledExactlyOnceWith({ id: "build-1" }, { signal: expect.any(AbortSignal), timeoutMs: 15_000 });
  const cancelSignal = vi.mocked(authClient.cancelAgentImageBuild).mock.calls[0][1]?.signal;
  expect(cancelSignal?.aborted).toBe(false);
  expect(cancelSignal).not.toBe(pollSignal);
  expect(Date.now()).toBe(beforeCancel);
  expect(view.queryByRole("alert")).toBeNull();
  expect(view.onBusyChange).toHaveBeenLastCalledWith(true);
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => { resolveCancel(new AgentImageBuild({ ...building, status: "cancelled" })); });
  expect(view.getByRole("status").textContent).toBe("Сборка отменена");
  expect(view.queryByRole("alert")).toBeNull();
  expect(view.onBusyChange).toHaveBeenLastCalledWith(false);
  expect(vi.getTimerCount()).toBe(0);
});

test("a Connect deadline error remains visible and can be retried", async () => {
  vi.mocked(authClient.getAgentImageBuild)
    .mockRejectedValueOnce(new ConnectError("deadline exceeded", Code.DeadlineExceeded));
  const view = await mountForm();
  expect(view.getByRole("alert").textContent).toBe("deadline exceeded");
  expect(view.onBusyChange).toHaveBeenLastCalledWith(true);
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Повторить запрос" })); });
  expect(view.queryByRole("alert")).toBeNull();
  expect(view.onBusyChange).toHaveBeenLastCalledWith(false);
});

test("unmount aborts requests and ignores late success", async () => {
  let resolveGet!: (build: AgentImageBuild) => void;
  vi.mocked(authClient.getAgentImageBuild).mockImplementationOnce(() => new Promise((resolve) => { resolveGet = resolve; }));
  const view = await mountForm();
  const signal = vi.mocked(authClient.getAgentImageBuild).mock.calls[0][1]?.signal;
  view.unmount();
  expect(signal?.aborted).toBe(true);
  const busyCalls = view.onBusyChange.mock.calls.length;
  await act(async () => { resolveGet(new AgentImageBuild({ id: "late", status: "succeeded" })); });
  expect(view.onSucceeded).not.toHaveBeenCalled();
  expect(view.onBusyChange).toHaveBeenCalledTimes(busyCalls);
  expect(vi.getTimerCount()).toBe(0);
});

test("a failed start is reconciled before another mutation and retains the draft", async () => {
  vi.mocked(authClient.startAgentImageBuild).mockRejectedValueOnce(new Error("offline"));
  const view = await mountForm();
  const editor = view.getByLabelText("Скрипт установки") as HTMLTextAreaElement;
  const name = view.getByLabelText("Название") as HTMLInputElement;
  fireEvent.change(name, { target: { value: "draft-tools" } });
  fireEvent.change(editor, { target: { value: "echo draft" } });
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Собрать" })); });
  expect(view.getByRole("alert").textContent).toContain("Не удалось запустить сборку");
  expect(view.onBusyChange).toHaveBeenLastCalledWith(true);
  expect(editor.readOnly).toBe(true);
  vi.mocked(authClient.getAgentImageBuild).mockResolvedValue(new AgentImageBuild({ id: "accepted", name: "server-tools", script: "server script", status: "building" }));
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Повторить запрос" })); });
  expect(editor.value).toBe("echo draft");
  expect(name.value).toBe("draft-tools");
  expect(view.queryByRole("alert")).toBeNull();
  expect(view.getByRole("button", { name: "Отменить" })).toBeTruthy();
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

test("a failed poll pauses until retry and resumes only for an active build", async () => {
  vi.mocked(authClient.getAgentImageBuild)
    .mockResolvedValueOnce(new AgentImageBuild({ id: "build-1", status: "building" }))
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce(new AgentImageBuild({ id: "build-1", status: "interrupted" }));
  const view = await mountForm();
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(view.getByRole("alert")).toBeTruthy();
  expect(view.onBusyChange).toHaveBeenLastCalledWith(true);
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Повторить запрос" })); });
  expect(view.getByRole("status").textContent).toContain("Сборка прервана");
  expect(view.onBusyChange).toHaveBeenLastCalledWith(false);
  expect(vi.getTimerCount()).toBe(0);
});

test("refresh failure keeps mutations locked until an explicit retry succeeds", async () => {
  const build = new AgentImageBuild({ id: "done", script: "echo saved", status: "succeeded" });
  vi.mocked(authClient.getAgentImageBuild).mockResolvedValue(build);
  const onSucceeded = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(undefined);
  const onBusyChange = vi.fn();
  const view = render(<ImageBuildForm disabled={false} onSucceeded={onSucceeded} onBusyChange={onBusyChange} />);
  await act(async () => {});
  expect(view.getByRole("alert").textContent).toContain("не удалось обновить список и квоту");
  expect(onBusyChange).toHaveBeenLastCalledWith(true);
  await act(async () => { fireEvent.click(view.getByRole("button", { name: "Повторить запрос" })); });
  expect(onSucceeded).toHaveBeenCalledTimes(2);
  expect(onBusyChange).toHaveBeenLastCalledWith(false);
});

test("parent blocks mutations during a build and refreshes images and quota on success", async () => {
  vi.mocked(authClient.getAgentRuntime).mockResolvedValue(new AgentRuntimeSettings({
    mode: "docker", runningMode: "docker", editable: true,
    contexts: [{ name: "default" }],
  }));
  vi.mocked(authClient.getAgentImages)
    .mockResolvedValueOnce(new AgentImagesSettings({ images: [{ image: "existing:v1" }], quotaBytes: 2048n }))
    .mockResolvedValueOnce(new AgentImagesSettings({ images: [{ image: "existing:v1" }, { image: "agent:built", name: "python-tools" }], usedBytes: 1024n, quotaBytes: 2048n }));
  vi.mocked(authClient.getAgentImageBuild)
    .mockResolvedValueOnce(new AgentImageBuild({ id: "build-1", status: "building" }))
    .mockResolvedValueOnce(new AgentImageBuild({ id: "build-1", status: "succeeded" }));
  const view = render(<EnvironmentSection />);
  await act(async () => {});
  expect((view.getByRole("button", { name: "Local" }) as HTMLButtonElement).disabled).toBe(true);
  expect((view.getByRole("radio") as HTMLInputElement).disabled).toBe(true);
  expect((view.getByRole("button", { name: "Удалить" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(view.getByText("existing:v1")).toBeTruthy();
  expect(view.getByText("python-tools")).toBeTruthy();
  expect(view.queryByText("agent:built")).toBeNull();
  expect(view.getByText("занято 1.0 КБ из 2.0 КБ")).toBeTruthy();
  expect(authClient.getAgentImages).toHaveBeenCalledTimes(2);
  expect(authClient.getAgentImages).toHaveBeenLastCalledWith({}, { signal: expect.any(AbortSignal), timeoutMs: 15_000 });
  expect((view.getByRole("button", { name: "Local" }) as HTMLButtonElement).disabled).toBe(false);
});

test.each([
  ["local", "local", false],
  ["docker", "local", true],
  ["docker", "docker", true],
] as const)("does not load the build for mode=%s, runningMode=%s, restartRequired=%s", async (mode, runningMode, restartRequired) => {
  vi.mocked(authClient.getAgentRuntime).mockResolvedValue(new AgentRuntimeSettings({ mode, runningMode, restartRequired }));
  vi.mocked(authClient.getAgentImages).mockResolvedValue(new AgentImagesSettings());
  const view = render(<EnvironmentSection />);
  await act(async () => {});
  expect(view.queryByLabelText("Скрипт установки")).toBeNull();
  expect(authClient.getAgentImageBuild).not.toHaveBeenCalled();
});

test("Docker environment only offers a script, not an arbitrary image reference", async () => {
  vi.mocked(authClient.getAgentRuntime).mockResolvedValue(new AgentRuntimeSettings({ mode: "docker", runningMode: "docker" }));
  vi.mocked(authClient.getAgentImages).mockResolvedValue(new AgentImagesSettings({ defaultImage: "brigade/agent:latest" }));
  const view = render(<EnvironmentSection />);
  await act(async () => {});
  expect(view.getByLabelText("Скрипт установки")).toBeTruthy();
  expect(view.queryByText("Новый образ")).toBeNull();
  expect(view.queryByPlaceholderText("ghcr.io/username/agent:v1")).toBeNull();
  expect(view.queryByRole("button", { name: "Добавить" })).toBeNull();
});
