import { readFileSync } from "node:fs";
import { HttpAgent } from "@ag-ui/client";
import { expect, it } from "vitest";

it("reads Brigade SSE through HttpAgent and preserves replay, tools and terminal state", async () => {
  const fixture = new URL("../../../../backend/internal/transport/agui/testdata/run-events.json", import.meta.url);
  const frames: { type: string; [field: string]: unknown }[] = JSON.parse(readFileSync(fixture, "utf8"));
  const parts = [{ type: "text" as const, text: "question" }];
  const agent = new HttpAgent({
    url: "/api/ag-ui/run",
    threadId: "thread",
    initialMessages: [{ id: "u", role: "user", content: parts }],
    fetch: async (_url, init) => {
      expect(init.method).toBe("POST");
      expect(JSON.parse(String(init.body))).toMatchObject({
        threadId: "thread", runId: "run", protocolVersion: "1.0",
        messages: [{ id: "u", role: "user", content: parts }],
      });
      return new Response(frames.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(": keepalive\n\n"), {
        headers: { "Content-Type": "text/event-stream" },
      });
    },
  });

  const seen: string[] = [];
  const result = await agent.runAgent({ runId: "run" }, {
    onEvent: ({ event }) => { seen.push(event.type); },
    onRunFinishedEvent: (end) => {
      expect(end.outcome).toBe("success");
      if (end.outcome === "success") expect(end.pendingToolCallIds).toEqual([]);
    },
  });
  expect(seen).toEqual(frames.map(({ type }) => type));
  expect(result.result).toEqual({ stopReason: "end_turn" });
  expect(agent.state).toEqual({ plan: [] });
  expect(agent.messages).toEqual(expect.arrayContaining([
    expect.objectContaining({ id: "reason", role: "reasoning", content: "thinking" }),
    expect.objectContaining({ toolCallId: "call", role: "tool", content: "" }),
    expect.objectContaining({ toolCallId: "silent", role: "tool", content: "" }),
    expect.objectContaining({ id: "reply", role: "assistant", content: "ответ" }),
  ]));
});
