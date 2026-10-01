import { expect, test } from "vitest";
import { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { selectedMessageIds } from "./contextSelection";

const messages = Array.from({ length: 15 }, (_, index) => new AcpMessage({
  id: `m${index}`, content: `Сообщение ${index}`, delivery: "received",
}));
messages.push(new AcpMessage({ id: "draft", delivery: "draft" }));
messages.push(new AcpMessage({ id: "deleted", delivery: "deleted" }));
messages.push(new AcpMessage({ id: "stale", delivery: "stale" }));

test("recent context selects the last ten messages, not the whole history", () => {
  expect(selectedMessageIds(messages, { kind: "recent", count: 10 }))
    .toEqual(Array.from({ length: 10 }, (_, index) => `m${index + 5}`));
});

test("manual context keeps original message order and excludes drafts, stale and deleted messages", () => {
  expect(selectedMessageIds(messages, { kind: "custom", ids: new Set(["m9", "m1", "draft", "deleted", "stale"]) }))
    .toEqual(["m1", "m9"]);
});
