import { useEffect, useRef, useState } from "react";
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
import { AlertCircle, Loader2 } from "lucide-react";
import { pluginClient } from "@/api/client";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

async function mcp<T>(sessionId: string, method: string, params: unknown): Promise<T> {
  const response = await pluginClient.mCP({
    sessionId,
    method,
    paramsJson: encoder.encode(JSON.stringify(params)),
  });
  return JSON.parse(decoder.decode(response.resultJson)) as T;
}

type AppState = { html: string; tool: Tool; result: CallToolResult };

export function McpAppFrame({ sessionId, entryTool }: { sessionId: string; entryTool: string }) {
  const iframe = useRef<HTMLIFrameElement>(null);
  const [state, setState] = useState<AppState | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setState(null);
    setError("");
    void (async () => {
      try {
        const listed = await mcp<{ tools: Tool[] }>(sessionId, "tools/list", {});
        const tool = listed.tools.find((item) => item.name === entryTool);
        if (!tool) throw new Error(`MCP tool ${entryTool} не найден`);
        const resourceUri = getToolUiResourceUri(tool);
        if (!resourceUri) throw new Error(`MCP tool ${entryTool} не объявляет ui:// resource`);
        const result = await mcp<CallToolResult>(sessionId, "tools/call", { name: entryTool, arguments: {} });
        const resource = await mcp<ReadResourceResult>(sessionId, "resources/read", { uri: resourceUri });
        const html = resource.contents.find((item) => "text" in item)?.text;
        if (!html) throw new Error(`MCP App resource ${resourceUri} не содержит HTML`);
        if (!cancelled) setState({ html, tool, result });
      } catch (reason) {
        if (!cancelled) setError(reason instanceof Error ? reason.message : "Не удалось открыть MCP App");
      }
    })();
    return () => { cancelled = true; };
  }, [entryTool, sessionId]);

  useEffect(() => {
    const frame = iframe.current;
    if (!frame || !state) return;
    let bridge: AppBridge | null = null;
    const connect = () => {
      const target = frame.contentWindow;
      if (!target) return;
      bridge = new AppBridge(
        null,
        { name: "Brigade", version: "1" },
        { openLinks: {}, downloadFile: {}, serverTools: {}, serverResources: {}, logging: {} },
        {
          hostContext: {
            toolInfo: { tool: state.tool },
            theme: "dark",
            displayMode: "fullscreen",
            availableDisplayModes: ["fullscreen"],
            locale: navigator.language,
            timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
            platform: "web",
            userAgent: "Brigade",
          },
        },
      );
      bridge.oncalltool = (params) => mcp<CallToolResult>(sessionId, "tools/call", params);
      bridge.onlistresources = (params) => mcp<ListResourcesResult>(sessionId, "resources/list", params);
      bridge.onlistresourcetemplates = (params) => mcp<ListResourceTemplatesResult>(sessionId, "resources/templates/list", params);
      bridge.onreadresource = (params) => mcp<ReadResourceResult>(sessionId, "resources/read", params);
      bridge.onlistprompts = (params) => mcp<ListPromptsResult>(sessionId, "prompts/list", params);
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
        void bridge?.sendToolInput({ arguments: {} });
        void bridge?.sendToolResult(state.result);
      };
      const transport = new PostMessageTransport(target, target);
      void bridge.connect(transport).catch((reason) => {
        setError(reason instanceof Error ? reason.message : "Не удалось подключить MCP App");
      });
    };
    frame.addEventListener("load", connect, { once: true });
    frame.srcdoc = state.html;
    return () => {
      frame.removeEventListener("load", connect);
      void bridge?.close();
    };
  }, [sessionId, state]);

  if (error) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-destructive">
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
    <iframe
      ref={iframe}
      title={state.tool.title || state.tool.name}
      sandbox="allow-scripts allow-forms allow-downloads"
      className="h-full w-full border-0 bg-background"
    />
  );
}
