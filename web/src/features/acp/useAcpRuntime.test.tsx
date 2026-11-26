// @vitest-environment happy-dom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { GetHistoryResponse, GetStatusResponse } from "@/api/gen/brigade/v1/acp_pb";
import { importAcpHistory, useAcpRuntime } from "./useAcpRuntime";

const rpc = vi.hoisted(() => ({
  getHistory: vi.fn<() => Promise<GetHistoryResponse>>(),
  getStatus: vi.fn<() => Promise<GetStatusResponse>>(),
  listWorkflows: vi.fn().mockResolvedValue({ workflows: [] }),
  resolvePermission: vi.fn().mockResolvedValue({}),
  cancel: vi.fn().mockResolvedValue({}),
}));
const auth = vi.hoisted(() => ({
  getAccessToken: vi.fn<() => string | null>(),
  refreshSession: vi.fn(),
}));
vi.mock("@/api/client", () => ({ acpClient: rpc, refreshSession: auth.refreshSession }));
vi.mock("@/features/auth/AuthContext", () => ({ useAuth: () => auth }));

const sessionId = "test-acp-session";

test("history preserves explicit timestamps without inventing dates for legacy messages", () => {
  const history = importAcpHistory([
    { id: "dated", role: "user", content: "Вопрос", createdAt: 1700000000n },
    { id: "legacy", role: "assistant", content: "Ответ" },
    { id: "invalid", role: "user", content: "Другой", createdAt: Infinity },
  ]);
  expect(history.messages.find((entry) => entry.message.id === "dated")?.message.metadata?.custom?.createdAt).toBe("2023-11-14T22:13:20.000Z");
  expect(history.messages.find((entry) => entry.message.id === "legacy")?.message.metadata?.custom?.createdAt).toBeUndefined();
  expect(history.messages.find((entry) => entry.message.id === "invalid")?.message.metadata?.custom?.createdAt).toBeUndefined();
});
const fetchMock = vi.fn<typeof fetch>();
type RunInput = {
  threadId: string;
  runId: string;
  messages: { role: string; content: unknown }[];
  forwardedProps?: { runConfig?: { brigadeReplay?: boolean } };
};

// Подменяем только HTTP: фрейминг SSE, проверку схемы и редукцию событий выполняет HttpAgent.
function openStream(input: RequestInfo | URL, init?: RequestInit) {
  expect(String(input)).toBe("/api/ag-ui/run");
  expect(init?.method?.toUpperCase()).toBe("POST");
  const body: RunInput = JSON.parse(String(init?.body));
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const signal = init?.signal;
  const stream = new ReadableStream<Uint8Array>({
    start(value) {
      controller = value;
      signal?.addEventListener("abort", () => value.error(new DOMException("Aborted", "AbortError")), { once: true });
    },
  });
  const emit = (event: Record<string, unknown>) =>
    controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`));
  const ids = { threadId: body.threadId, runId: body.runId };
  emit({ type: "RUN_STARTED", ...ids });
  return {
    body, signal, emit,
    response: new Response(stream, { headers: { "Content-Type": "text/event-stream" } }),
    text(messageId: string, delta: string) {
      emit({ type: "TEXT_MESSAGE_START", role: "assistant", messageId });
      emit({ type: "TEXT_MESSAGE_CONTENT", messageId, delta });
      emit({ type: "TEXT_MESSAGE_END", messageId });
    },
    finish() {
      emit({ type: "RUN_FINISHED", ...ids });
      controller.close();
    },
  };
}

let runs: ReturnType<typeof openStream>[];
beforeEach(() => {
  vi.clearAllMocks();
  runs = [];
  auth.getAccessToken.mockReturnValue(null);
  auth.refreshSession.mockResolvedValue(undefined);
  rpc.getHistory.mockResolvedValue(new GetHistoryResponse());
  rpc.getStatus.mockResolvedValue(new GetStatusResponse());
  fetchMock.mockReset().mockImplementation(async (input, init) => {
    const run = openStream(input, init);
    runs.push(run);
    return run.response;
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function mount() {
  const view = renderHook(() => useAcpRuntime(sessionId));
  const thread = view.result.current.runtime.thread;
  await waitFor(() => {
    expect(rpc.getHistory).toHaveBeenCalledWith({ threadId: sessionId });
    expect(thread.getState().isLoading).toBe(false);
  });
  return { ...view, thread };
}

test("Stop cancels the active turn once and the next question gets its own answer", async () => {
  const { thread } = await mount();
  act(() => { void thread.append("Первый вопрос"); });
  await waitFor(() => expect(runs).toHaveLength(1));
  act(() => runs[0].text("first-answer", "Начало первого ответа"));
  await waitFor(() => expect(thread.getState().messages.at(-1)?.content).toMatchObject([
    { type: "text", text: "Начало первого ответа" },
  ]));
  act(() => { thread.cancelRun(); });
  act(() => { void thread.append("Второй вопрос"); });
  await waitFor(() => expect(runs).toHaveLength(2));
  expect(rpc.cancel).toHaveBeenCalledExactlyOnceWith({ threadId: sessionId });
  expect(runs[0].signal?.aborted).toBe(true);
  expect(runs[1].body.threadId).toBe(sessionId);
  expect(runs[1].body.runId).not.toBe(runs[0].body.runId);
  expect(runs[1].body.messages.at(-1)).toMatchObject({ role: "user", content: "Второй вопрос" });
  expect(runs[1].body.forwardedProps?.runConfig?.brigadeReplay).not.toBe(true);
  act(() => {
    runs[1].text("second-answer", "Ответ на второй вопрос");
    runs[1].finish();
  });
  await waitFor(() => {
    expect(thread.getState().isRunning).toBe(false);
    expect(thread.getState().messages.at(-1)).toMatchObject({
      role: "assistant", content: [{ type: "text", text: "Ответ на второй вопрос" }],
    });
  });
});

test.each(["resolved", "rejected"] as const)(
  "Stop with a delayed %s Cancel RPC gates the next POST",
  async (outcome) => {
    let settleCancel!: () => void;
    rpc.cancel.mockImplementationOnce(() => new Promise<void>((resolve, reject) => {
      settleCancel = () => outcome === "resolved" ? resolve() : reject(new Error("Cancel failed"));
    }));
    const { thread } = await mount();
    act(() => { void thread.append("Первый вопрос"); });
    await waitFor(() => expect(runs).toHaveLength(1));

    act(() => { thread.cancelRun(); });
    // Не ждём завершения Cancel между двумя действиями пользователя.
    await act(async () => { void thread.append("Отложенный вопрос"); });
    expect(rpc.cancel).toHaveBeenCalledExactlyOnceWith({ threadId: sessionId });
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await act(async () => { settleCancel(); });
    if (outcome === "rejected") {
      await waitFor(() => expect(thread.getState().isRunning).toBe(false));
      expect(fetchMock).toHaveBeenCalledTimes(1);
      act(() => { void thread.append("Явная повторная попытка"); });
    }
    await waitFor(() => expect(runs).toHaveLength(2));
    expect(runs[1].body.messages.at(-1)).toMatchObject({
      role: "user",
      content: outcome === "resolved" ? "Отложенный вопрос" : "Явная повторная попытка",
    });
    act(() => { runs[1].text("answer", "Готово"); runs[1].finish(); });
    await waitFor(() => {
      expect(thread.getState().isRunning).toBe(false);
      expect(thread.getState().messages.at(-1)?.content).toMatchObject([{ text: "Готово" }]);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  },
);

test("leaving a page detaches its live SSE without cancelling the server turn", async () => {
  const { thread, unmount } = await mount();
  act(() => { void thread.append("Продолжай в фоне"); });
  await waitFor(() => expect(runs).toHaveLength(1));
  expect(runs[0].signal?.aborted).toBe(false);
  unmount();
  await waitFor(() => expect(runs[0].signal?.aborted).toBe(true));
  expect(rpc.cancel).not.toHaveBeenCalled();
});

test("restores text and tool results from RPC without submitting a prompt", async () => {
  rpc.getHistory.mockResolvedValue(new GetHistoryResponse({
    commands: [{ name: "inspect", description: "Осмотр" }],
    messages: [
      { id: "q", role: "user", content: "Что в файле?" },
      { id: "t", role: "tool_call", toolName: "read", argsText: '{"path":"a"}', result: "ok" },
      { id: "a", role: "assistant", content: "Готово" },
    ],
  }));
  const { result, thread } = await mount();
  // Адаптер может объединить соседние записи assistant. Проверяем порядок и владельца частей.
  await waitFor(() => {
    const parts = thread.getState().messages.flatMap(({ role, content }) =>
      content.map((part) => ({ role, ...part })),
    );
    expect(parts).toMatchObject([
      { role: "user", type: "text", text: "Что в файле?" },
      { role: "assistant", type: "tool-call", toolName: "read", toolCallId: "t", args: { path: "a" }, result: "ok" },
      { role: "assistant", type: "text", text: "Готово" },
    ]);
  });
  expect(result.current.commands).toMatchObject([{ name: "inspect" }]);
  expect(fetchMock).not.toHaveBeenCalled();
});

test("replay forwards brigadeReplay at the wire path read by the Go handler", async () => {
  rpc.getHistory.mockResolvedValue(new GetHistoryResponse({
    messages: [{ id: "q", role: "user", content: "Исходный вопрос" }],
  }));
  const { thread } = await mount();
  await waitFor(() => expect(thread.getState().messages[0]?.id).toBe("q"));
  act(() => { thread.startRun({ parentId: "q", runConfig: { custom: { brigadeReplay: true } } }); });
  await waitFor(() => expect(runs).toHaveLength(1));
  expect(runs[0].body).toMatchObject({ threadId: sessionId, forwardedProps: { runConfig: { brigadeReplay: true } } });
  act(() => { runs[0].text("replayed", "Ответ из фонового turn"); runs[0].finish(); });
  await waitFor(() => expect(thread.getState().messages).toMatchObject([
    { role: "user", content: [{ text: "Исходный вопрос" }] },
    { role: "assistant", content: [{ text: "Ответ из фонового turn" }] },
  ]));
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

test("delivers a live permission decision and ignores a stale status after the reply", async () => {
  const { thread, result } = await mount();
  const request = { id: "approval", title: "Читать файл?", options: [{ optionId: "allow-read", kind: "allow_once" }] };
  act(() => { void thread.append("Прочитай файл"); });
  await waitFor(() => expect(runs).toHaveLength(1));
  act(() => runs[0].emit({ type: "CUSTOM", name: "permission_request", value: request }));
  await waitFor(() => expect(result.current.permission).toMatchObject(request));
  act(() => result.current.resolvePermission(request.id, "allow-read"));
  expect(rpc.resolvePermission).toHaveBeenCalledExactlyOnceWith({ threadId: sessionId, id: "approval", decision: "allow-read" });
  expect(result.current.permission).toBeNull();
  rpc.getStatus.mockResolvedValue(new GetStatusResponse({ seq: 7n, pendingPermissions: [JSON.stringify(request)] }));
  act(() => result.current.refreshStatus());
  await waitFor(() => expect(result.current.status.seq).toBe(7));
  expect(result.current.permission).toBeNull();
  act(() => runs[0].finish());
  await waitFor(() => expect(thread.getState().isRunning).toBe(false));
});

test("restores a pending permission from status before any SSE run", async () => {
  rpc.getStatus.mockResolvedValue(new GetStatusResponse({
    pendingPermissions: [JSON.stringify({ id: "offline", title: "Продолжить?" })],
  }));
  const { result } = await mount();
  await waitFor(() => expect(result.current.permission).toMatchObject({
    id: "offline", options: [{ optionId: "allow" }, { optionId: "reject" }],
  }));
  expect(fetchMock).not.toHaveBeenCalled();
});

test("retries the same request with the refreshed cookie and without the stale bearer", async () => {
  auth.getAccessToken.mockReturnValue("old-access");
  fetchMock.mockResolvedValueOnce(new Response(null, { status: 401 }));
  const { thread } = await mount();
  act(() => { void thread.append("Проверка доступа"); });
  await waitFor(() => expect(runs).toHaveLength(1));
  const [first, retry] = fetchMock.mock.calls.map(([, init]) => init);
  expect(auth.refreshSession).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(first?.credentials).toBe("include");
  expect(new Headers(first?.headers).get("authorization")).toBe("Bearer old-access");
  expect(retry?.credentials).toBe("include");
  expect(new Headers(retry?.headers).has("authorization")).toBe(false);
  expect(retry?.body).toBe(first?.body);
  act(() => { runs[0].text("answer", "Доступ есть"); runs[0].finish(); });
  await waitFor(() => expect(thread.getState().messages.at(-1)?.content).toMatchObject([{ text: "Доступ есть" }]));
});

test("a second 401 ends the cookie-only run without another refresh", async () => {
  fetchMock.mockResolvedValueOnce(new Response("Unauthorized", { status: 401 }))
    .mockResolvedValueOnce(new Response("Unauthorized", { status: 401 }));
  const { thread } = await mount();
  act(() => { void thread.append("Вопрос после перезагрузки"); });
  await waitFor(() => {
    expect(thread.getState().messages.at(-1)).toMatchObject({
      role: "assistant", status: { type: "incomplete", reason: "error" },
    });
    expect(thread.getState().isRunning).toBe(false);
  });
  expect(auth.refreshSession).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  for (const [, init] of fetchMock.mock.calls) {
    expect(init?.credentials).toBe("include");
    expect(new Headers(init?.headers).has("authorization")).toBe(false);
  }
});
