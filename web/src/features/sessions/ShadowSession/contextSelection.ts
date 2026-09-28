import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";

export type ContextSelection =
  | { kind: "recent"; count: number }
  | { kind: "all" }
  | { kind: "custom"; ids: Set<string> };

export function canUseAsContext(message: AcpMessage): boolean {
  return !["draft", "sending", "failed", "uncertain"].includes(message.delivery);
}

export function selectedMessageIds(messages: AcpMessage[], selection: ContextSelection): string[] {
  const eligible = messages.filter(canUseAsContext);
  if (selection.kind === "all") return eligible.map((message) => message.id);
  if (selection.kind === "recent") return eligible.slice(-selection.count).map((message) => message.id);
  return eligible.filter((message) => selection.ids.has(message.id)).map((message) => message.id);
}
