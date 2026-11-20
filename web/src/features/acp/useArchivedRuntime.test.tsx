// @vitest-environment happy-dom
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useArchivedRuntime } from "./useArchivedRuntime";
import { ArchivedHistoryResponse } from "@/api/gen/brigade/v1/archive_pb";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

test("archive imports completed calls and reasoning without starting an agent", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { messages: snapshot } = new ArchivedHistoryResponse({
    messages: [
      { role: "user", id: "q", content: "Проверь" },
      { role: "reasoning", id: "why", content: "Проверяю" },
      { role: "tool_call", id: "call", toolName: "read", argsText: '{"file":"a"}', result: '{"ok":true}' },
      { role: "tool_call", id: "empty", toolName: "noop" },
      { role: "assistant", id: "answer", content: "Готово" },
    ],
  });
  const { result } = renderHook(() => useArchivedRuntime("archived", snapshot));
  await waitFor(() => expect(result.current.runtime.thread.getState().isLoading).toBe(false));
  const messages = result.current.runtime.thread.getState().messages;
  expect(messages[0]).toMatchObject({ id: "q", role: "user" });
  const parts = messages.flatMap((message) => message.role === "assistant" ? message.content : []);
  expect(parts).toMatchObject([
    { type: "reasoning", text: "Проверяю" },
    { type: "tool-call", toolCallId: "call", args: { file: "a" }, result: { ok: true } },
    { type: "tool-call", toolCallId: "empty", result: "" },
    { type: "text", text: "Готово" },
  ]);
  expect(fetch).not.toHaveBeenCalled();
});
