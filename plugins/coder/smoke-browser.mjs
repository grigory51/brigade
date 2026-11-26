import { createServer } from "node:http";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";
import { build } from "./ui/node_modules/esbuild/lib/main.js";
import { Client } from "./ui/node_modules/@modelcontextprotocol/sdk/dist/esm/client/index.js";
import { StdioClientTransport } from "./ui/node_modules/@modelcontextprotocol/sdk/dist/esm/client/stdio.js";

// Изолированный browser smoke: настоящий MCP binary и официальный AppBridge, без ACP.
const root = dirname(fileURLToPath(import.meta.url));
const temporary = await mkdtemp(join(tmpdir(), "brigade-coder-browser-"));
execFileSync("unzip", ["-q", join(root, "brigade-coder.mcpb"), "-d", temporary]);
await mkdir(join(temporary, "workspace"));
const client = new Client({ name: "coder-browser-smoke", version: "1" });
await client.connect(new StdioClientTransport({
  command: join(temporary, "server", "brigade-coder"),
  env: { ...process.env, BRIGADE_WORKSPACE: join(temporary, "workspace") },
}));
const { tools } = await client.listTools();
const tool = tools.find((item) => item.name === "coder.open");
const result = await client.callTool({ name: "coder.open", arguments: {} });
const resource = await client.readResource({ uri: "ui://coder/app.html" });
const html = resource.contents[0].text;
if (!html || !tool) throw new Error("Bundle is missing its entry tool or HTML resource");

const host = await build({
  absWorkingDir: join(root, "ui"), bundle: true, write: false, format: "esm",
  stdin: { resolveDir: join(root, "ui"), contents: `
import { AppBridge, PostMessageTransport } from "@modelcontextprotocol/ext-apps/app-bridge";
window.audit = [];
window.addEventListener("message", event => {
  if (event.source === frame.contentWindow) window.audit.push({type: "message", method: event.data?.method, at: performance.now()});
});
const frame = document.querySelector("iframe");
const {tool, result, html} = await fetch("/boot").then(response => response.json());
const bridge = new AppBridge(null, {name: "Brigade smoke", version: "1"}, {
  openLinks: {}, downloadFile: {}, serverTools: {}, serverResources: {}, logging: {}, message: {text: {}}
}, {hostContext: {
  toolInfo: {tool}, theme: "dark", displayMode: "fullscreen", availableDisplayModes: ["fullscreen"],
  locale: "ru", timeZone: "UTC", platform: "web", userAgent: "Brigade"
}});
bridge.oncalltool = params => fetch("/tool", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify(params)}).then(response => response.json());
bridge.onerror = error => { window.audit.push({type: "bridge-error", error: String(error)}); console.error(error); };
bridge.oninitialized = () => {
  window.audit.push({type: "initialized", at: performance.now()});
  void bridge.sendToolInput({arguments: {}});
  void bridge.sendToolResult(result);
  void bridge.sendToolResult({content: [], structuredContent: {brigadeHost: {coding: {getCI: true}}}});
};
const connect = async () => {
  window.audit.push({type: "connect", at: performance.now()});
  await bridge.connect(new PostMessageTransport(frame.contentWindow, frame.contentWindow));
};
frame.addEventListener("load", () => window.audit.push({type: "load", at: performance.now()}));
if (new URL(location.href).searchParams.get("connect") === "before") await connect();
else frame.addEventListener("load", () => void connect(), {once: true});
frame.srcdoc = html;
` },
});

const server = createServer(async (request, response) => {
  try {
    response.setHeader("Cache-Control", "no-store");
    if (request.url === "/boot") {
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify({ tool, result, html }));
    } else if (request.url === "/host.js") {
      response.setHeader("Content-Type", "text/javascript");
      response.end(host.outputFiles[0].text);
    } else if (request.url === "/tool" && request.method === "POST") {
      let body = "";
      for await (const chunk of request) {
        body += chunk;
        if (body.length > 65536) throw new Error("Request too large");
      }
      const params = JSON.parse(body);
      if (!["coder.state", "coder.changes", "coder.git_history", "coder.git_details", "coder.git_compare", "coder.git_diff"].includes(params.name)) throw new Error("Smoke only permits read-only Coder tools");
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify(await client.callTool(params)));
    } else {
      response.setHeader("Content-Type", "text/html");
      response.end('<!doctype html><meta charset="utf-8"><title>Coder real SDK smoke</title><style>body{margin:0;background:#151515}iframe{border:0;width:600px;height:720px}</style><iframe sandbox="allow-scripts allow-forms allow-downloads" title="Coder"></iframe><script type="module" src="/host.js"></script>');
    }
  } catch (error) {
    response.statusCode = 500;
    response.end(String(error));
  }
});
server.listen(0, "127.0.0.1", () => console.log(`Coder browser smoke: http://127.0.0.1:${server.address().port}/`));
for (const signal of ["SIGINT", "SIGTERM"]) process.once(signal, async () => {
  server.close();
  await client.close();
  await rm(temporary, { recursive: true, force: true });
  process.exit(0);
});
