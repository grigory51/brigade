import { z } from "zod";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import { ConnectError } from "@connectrpc/connect";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { codingClient, pluginClient, sessionClient, workspaceClient } from "@/api/client";

const argument = z.string().refine((value) => !value.includes("\0"), "Аргумент содержит NUL");
const resultSchema = z.discriminatedUnion("kind", [
  z.object({ name: z.string().min(1), kind: z.literal("web"), url: z.string().url() }),
  z.object({
    name: z.string().min(1),
    kind: z.literal("terminal"),
    command: z.object({ argv: z.array(argument).min(1), cwd: argument.refine((value) => !value.startsWith("/"), "Рабочая директория должна быть относительно workspace") }),
  }),
]);
const stateSchema = z.object({ schemaVersion: z.literal(1), revision: z.number().int().nonnegative(), results: z.array(resultSchema) });
const openSchema = z.object({ stateTool: z.string().min(1), name: z.string().min(1), revision: z.number().int().nonnegative(), activate: z.boolean().optional() }).strict();

export type PluginResult = z.infer<typeof resultSchema>;
export type OpenPluginResult = { key: string; revision: number; result: PluginResult };

export const pluginHostCapabilities = { openResult: true, appendPrompt: true, coding: { getCI: true }, workspace: { list: true, read: true } } as const;

const workspacePathSchema = z.object({ path: z.string().max(4096).refine((path) =>
  path.length > 0 && !path.startsWith("/") && !path.includes("\\") && !path.includes("\0") && !path.split("/").includes(".."), "Путь должен быть внутри workspace") }).strict();
const workspaceListSchema = workspacePathSchema.extend({ pageToken: z.string().max(7).optional() });
const maxFileBytes = 1 << 20;

export async function callPluginHostTool(
  sessionId: string,
  params: { name: string; arguments?: Record<string, unknown> },
  onOpen: (result: OpenPluginResult, activate?: boolean) => void,
  signal: AbortSignal,
  stateTools: readonly string[],
  plugin: Pick<Plugin, "id" | "version">,
): Promise<CallToolResult | undefined> {
  if (!["brigade.coding.getCI", "brigade.openResult", "brigade.workspace.list", "brigade.workspace.read"].includes(params.name)) return undefined;
  try {
    signal.throwIfAborted();
    if (params.name === "brigade.workspace.list") {
      const input = workspaceListSchema.parse(params.arguments);
      const response = await workspaceClient.list({ sessionId, path: input.path, pageSize: 500, pageToken: input.pageToken ?? "" }, { signal });
      return { content: [], structuredContent: response.toJson({ emitDefaultValues: true }) as Record<string, unknown> };
    }
    if (params.name === "brigade.workspace.read") {
      const input = workspacePathSchema.parse(params.arguments);
      const { entry } = await workspaceClient.stat({ sessionId, path: input.path }, { signal });
      if (!entry || entry.directory) throw new Error("Выберите обычный файл");
      const response = await workspaceClient.read({ sessionId, path: input.path, offset: 0n, size: maxFileBytes }, { signal });
      const truncated = entry.size > BigInt(maxFileBytes) || response.content.byteLength === maxFileBytes;
      let text = "";
      let binary = response.content.includes(0);
      if (!binary) {
        try {
          // A bounded read may end inside a UTF-8 character; do not classify it as binary.
          text = new TextDecoder("utf-8", { fatal: true }).decode(response.content, { stream: truncated });
        } catch { binary = true; }
      }
      return { content: [], structuredContent: { text, binary, truncated } };
    }
    if (params.name === "brigade.coding.getCI") {
      const input = z.object({ projectPath: z.string().optional() }).strict().parse(params.arguments ?? {});
      const response = await codingClient.getCI({ sessionId, projectPath: input.projectPath ?? "." }, { signal });
      return { content: [], structuredContent: response.toJson({ emitDefaultValues: true }) as Record<string, unknown> };
    }
    const input = openSchema.parse(params.arguments);
    if (!stateTools.includes(input.stateTool)) throw new Error("State tool должен объявлять readOnlyHint");
    const response = await pluginClient.mCP({
      sessionId,
      expectedPlugin: { id: plugin.id, version: plugin.version },
      method: "tools/call",
      paramsJson: new TextEncoder().encode(JSON.stringify({ name: input.stateTool, arguments: {} })),
    }, { signal });
    const toolResult = JSON.parse(new TextDecoder().decode(response.resultJson)) as CallToolResult;
    if (toolResult.isError) throw new Error("Не удалось получить зарегистрированные результаты");
    const state = stateSchema.parse(toolResult.structuredContent);
    if (state.revision !== input.revision) throw new Error("Результат изменился. Обновите список и откройте его снова.");
    const result = state.results.find((item) => item.name === input.name);
    if (!result) throw new Error("Результат не найден");
    if (result.kind === "web") {
      const url = new URL(result.url);
      if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new Error("Preview должен использовать HTTP(S) без встроенных учётных данных");
      const registered = await sessionClient.listPreviews({ sessionId }, { signal });
      const matches = registered.previews.some((preview) => {
        const base = new URL(preview.url);
        const prefix = base.pathname.endsWith("/") ? base.pathname : `${base.pathname}/`;
        const routingQueryMatches = [...base.searchParams.keys()].every((key) =>
          JSON.stringify(url.searchParams.getAll(key)) === JSON.stringify(base.searchParams.getAll(key)),
        );
        return url.origin === base.origin && routingQueryMatches && (url.pathname === base.pathname || url.pathname.startsWith(prefix));
      });
      if (!matches) throw new Error("Preview не зарегистрирован в текущей сессии. Опубликуйте его через brigade preview.");
    } else if (!result.command.argv[0]) {
      throw new Error("Не указана команда для запуска");
    }
    if (signal.aborted) throw new Error("Открытие результата отменено");
    onOpen({ key: JSON.stringify([input.stateTool, input.name]), revision: input.revision, result }, input.activate);
    return { content: [], structuredContent: { opened: true } };
  } catch (error) {
    if (error instanceof ConnectError) {
      return { isError: true, content: [{ type: "text", text: error.rawMessage }], structuredContent: { error: {
        code: error.code, message: error.rawMessage,
        reason: error.metadata.get("coding-reason") ?? "",
        retryAfterSeconds: Number(error.metadata.get("retry-after")) || 0,
      } } };
    }
    return { isError: true, content: [{ type: "text", text: error instanceof Error ? error.message : "Не удалось выполнить действие" }] };
  }
}
