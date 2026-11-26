import { useEffect, useRef, useState } from "react";
import { useAui } from "@assistant-ui/react";
import {
  AppBridge,
  PostMessageTransport,
  getToolUiResourceUri,
} from "@modelcontextprotocol/ext-apps/app-bridge";
import type {
  CallToolResult,
  ListPromptsResult,
  ListResourcesResult,
  ListResourceTemplatesResult,
  ReadResourceResult,
  Tool,
} from "@modelcontextprotocol/sdk/types.js";
import { AlertCircle, Loader2, X } from "lucide-react";
import { pluginClient } from "@/api/client";
import type { Plugin } from "@/api/gen/brigade/v1/plugin_pb";
import { callPluginHostTool, pluginHostCapabilities, type OpenPluginResult } from "./host-tools";
import { PluginResultPanel } from "./PluginResultPanel";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

async function mcp<T>(sessionId: string, plugin: Pick<Plugin, "id" | "version">, method: string, params: unknown, signal: AbortSignal): Promise<T> {
  signal.throwIfAborted();
  const response = await pluginClient.mCP({
    sessionId,
    expectedPlugin: { id: plugin.id, version: plugin.version },
    method,
    paramsJson: encoder.encode(JSON.stringify(params)),
  }, { signal });
  return JSON.parse(decoder.decode(response.resultJson)) as T;
}

type AppState = { html: string; tool: Tool; result: CallToolResult; stateTools: string[] };

export function McpAppFrame({ sessionId, plugin }: { sessionId: string; plugin: Pick<Plugin, "id" | "version" | "entryTool"> }) {
  const { entryTool } = plugin;
  const iframe = useRef<HTMLIFrameElement>(null);
  const [state, setState] = useState<AppState | null>(null);
  const [error, setError] = useState("");
  const [{ results, activeResult }, setResultState] = useState<{ results: OpenPluginResult[]; activeResult: string | null }>({ results: [], activeResult: null });
  const { thread, composer } = useAui();
  const closeResult = (key: string) => {
    setResultState((current) => ({
      results: current.results.filter((result) => result.key !== key),
      activeResult: current.activeResult === key ? null : current.activeResult,
    }));
  };

  useEffect(() => {
    const controller = new AbortController();
    setState(null);
    setError("");
    void (async () => {
      try {
        const listed = await mcp<{ tools: Tool[] }>(sessionId, plugin, "tools/list", {}, controller.signal);
        const tool = listed.tools.find((item) => item.name === entryTool);
        if (!tool) throw new Error(`MCP tool ${entryTool} не найден`);
        const resourceUri = getToolUiResourceUri(tool);
        if (!resourceUri) throw new Error(`MCP tool ${entryTool} не объявляет ui:// resource`);
        const result = await mcp<CallToolResult>(sessionId, plugin, "tools/call", { name: entryTool, arguments: {} }, controller.signal);
        const resource = await mcp<ReadResourceResult>(sessionId, plugin, "resources/read", { uri: resourceUri }, controller.signal);
        const html = resource.contents.find((item) => "text" in item)?.text;
        if (!html) throw new Error(`MCP App resource ${resourceUri} не содержит HTML`);
        if (!controller.signal.aborted) setState({ html, tool, result, stateTools: listed.tools.filter((item) => item.annotations?.readOnlyHint === true).map((item) => item.name) });
      } catch (reason) {
        if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "Не удалось открыть MCP App");
      }
    })();
    return () => { controller.abort(); };
  }, [plugin, entryTool, sessionId]);

  useEffect(() => {
    const frame = iframe.current;
    if (!frame || !state) return;
    let bridge: AppBridge | null = null;
    const controller = new AbortController();
    const connect = async () => {
      const target = frame.contentWindow;
      if (!target) return;
      bridge = new AppBridge(
        null,
        { name: "Brigade", version: "1" },
        {
          openLinks: {},
          downloadFile: {},
          serverTools: {},
          serverResources: {},
          logging: {},
          message: { text: {} },
        },
        {
          hostContext: {
            toolInfo: { tool: state.tool },
            theme: "dark",
            displayMode: "inline",
            availableDisplayModes: ["inline"],
            locale: navigator.language,
            timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
            platform: "web",
            userAgent: "Brigade",
          },
        },
      );
      bridge.oncalltool = async (params) => {
        controller.signal.throwIfAborted();
        if (params.name === "brigade.appendPrompt") {
          const text = params.arguments?.text;
          if (typeof text !== "string" || !text.trim()) return { isError: true, content: [{ type: "text", text: "Не указан текст для composer" }] };
          const current = composer.getState().text;
          composer.setText(current ? `${current}\n\n${text}` : text);
          return { content: [], structuredContent: { appended: true } };
        }
        if (params.name === "brigade.cancel") {
          await thread.cancelRun();
          return { content: [] };
        }
        const hostResult = await callPluginHostTool(sessionId, params, (result, activate) => {
          setResultState((current) => {
            const existing = current.results.find((item) => item.key === result.key);
            if ((existing && existing.revision > result.revision) || (!existing && activate === false)) return current;
            return {
              results: existing ? current.results.map((item) => item.key === result.key ? result : item) : [...current.results, result],
              activeResult: activate === true || !existing ? result.key : current.activeResult,
            };
          });
        }, controller.signal, state.stateTools, plugin);
        if (hostResult) return hostResult;
        return mcp<CallToolResult>(sessionId, plugin, "tools/call", params, controller.signal);
      };
      bridge.onlistresources = (params) => mcp<ListResourcesResult>(sessionId, plugin, "resources/list", params, controller.signal);
      bridge.onlistresourcetemplates = (params) => mcp<ListResourceTemplatesResult>(sessionId, plugin, "resources/templates/list", params, controller.signal);
      bridge.onreadresource = (params) => mcp<ReadResourceResult>(sessionId, plugin, "resources/read", params, controller.signal);
      bridge.onlistprompts = (params) => mcp<ListPromptsResult>(sessionId, plugin, "prompts/list", params, controller.signal);
      bridge.onmessage = async ({ content }) => {
        controller.signal.throwIfAborted();
        const text = content
          .filter((item): item is { type: "text"; text: string } => item.type === "text")
          .map((item) => item.text)
          .join("\n")
          .trim();
        if (!text) return { isError: true };
        await thread.append({ role: "user", content: [{ type: "text", text }] });
        return {};
      };
      bridge.onopenlink = async ({ url }) => {
        const parsed = new URL(url, window.location.href);
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return { isError: true };
        window.open(parsed.href, "_blank", "noopener,noreferrer");
        return {};
      };
      bridge.ondownloadfile = async ({ contents }) => {
        for (const item of contents) {
          let url: string;
          let name: string;
          if (item.type === "resource_link") {
            const parsed = new URL(item.uri, window.location.href);
            if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return { isError: true };
            url = parsed.href;
            name = item.name;
          } else {
            const resource = item.resource;
            const bytes = "blob" in resource
              ? Uint8Array.from(atob(resource.blob), (char) => char.charCodeAt(0))
              : resource.text ?? "";
            url = URL.createObjectURL(new Blob([bytes], { type: resource.mimeType }));
            name = resource.uri.split("/").pop() || "download";
          }
          const link = document.createElement("a");
          link.href = url;
          link.download = name;
          link.click();
          if (url.startsWith("blob:")) window.setTimeout(() => URL.revokeObjectURL(url), 0);
        }
        return {};
      };
      bridge.oninitialized = () => {
        if (controller.signal.aborted) return;
        void bridge?.sendToolInput({ arguments: {} });
        void bridge?.sendToolResult(state.result);
        void bridge?.sendToolResult({
          content: [],
          structuredContent: { brigadeHost: pluginHostCapabilities },
        });
      };
      const transport = new PostMessageTransport(target, target);
      // App может отправить initialize до iframe load; listener должен быть готов до навигации.
      await bridge.connect(transport);
      if (!controller.signal.aborted) frame.srcdoc = state.html;
    };
    void connect().catch((reason) => {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "Не удалось подключить MCP App");
    });
    return () => {
      controller.abort();
      void bridge?.close();
    };
  }, [sessionId, plugin, state, thread, composer]);

  if (error) {
    return (
      <div role="alert" className="flex h-full items-center justify-center gap-2 text-sm text-destructive">
        <AlertCircle className="size-4" />
        <span>{error}</span>
      </div>
    );
  }
  if (!state) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        <span>Загружаю интерфейс…</span>
      </div>
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      {results.length > 0 && (
        <div className="flex h-12 shrink-0 items-center gap-1 overflow-x-auto border-b px-4 py-2" aria-label="Результаты приложения">
          <button type="button" aria-pressed={activeResult === null} onClick={() => setResultState((current) => ({ ...current, activeResult: null }))} className="h-8 shrink-0 rounded-[7px] px-3 text-sm hover:bg-accent aria-pressed:bg-secondary aria-pressed:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">Приложение</button>
          {results.map((item) => (
            <div key={item.key} className="flex shrink-0 items-center">
              <button type="button" aria-pressed={activeResult === item.key} onClick={() => setResultState((current) => ({ ...current, activeResult: item.key }))} className="h-8 max-w-44 truncate rounded-[7px] px-3 text-sm hover:bg-accent aria-pressed:bg-secondary aria-pressed:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">{item.result.name}</button>
              <button type="button" aria-label={`Закрыть ${item.result.name}`} onClick={() => {
                closeResult(item.key);
              }} className="flex size-8 items-center justify-center rounded-[7px] text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"><X aria-hidden="true" className="size-3" /></button>
            </div>
          ))}
        </div>
      )}
      <div hidden={activeResult !== null} className="min-h-0 flex-1">
        <iframe
          ref={iframe}
          title={state.tool.title || state.tool.name}
          sandbox="allow-scripts allow-forms allow-downloads"
          className="h-full w-full border-0 bg-background"
        />
      </div>
      {results.map((item) => (
        <div key={item.key} hidden={activeResult !== item.key} className="min-h-0 flex-1">
          <PluginResultPanel sessionId={sessionId} result={item.result} revision={item.revision} />
        </div>
      ))}
    </div>
  );
}
