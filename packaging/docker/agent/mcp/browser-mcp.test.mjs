import test from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { requestBrowser, socketPath } from "./browser-client.mjs";

test("MCP launches Chromium with clean HOME/env, keeps proxy, and exposes redacted diagnostics", {
  skip: process.env.BRIGADE_BROWSER_SMOKE !== "1", timeout: 60000,
}, async t => {
  const home = await mkdtemp(join(tmpdir(), "brigade-mcp-browser-"));
  t.after(() => rm(home, { recursive: true, force: true }));
  const seen = [];
  const proxy = http.createServer((req, res) => {
    seen.push(req.url);
    res.writeHead(403, { "Content-Type": "text/html" });
    res.end("<h1>Proxy test response</h1>");
  });
  // Chromium may probe unrelated HTTPS endpoints; never forward them to the internet.
  proxy.on("connect", (_req, socket) => socket.end("HTTP/1.1 502 Bad Gateway\r\n\r\n"));
  await new Promise(resolve => proxy.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise(resolve => { proxy.close(resolve); proxy.closeAllConnections(); }));
  const session = `mcp-browser-${randomUUID()}`;
  const socket = socketPath(session);
  const transport = new StdioClientTransport({
    command: process.execPath, args: [fileURLToPath(new URL("./brigade-tools.mjs", import.meta.url))],
    env: { HOME: home, PATH: process.env.PATH, BRIGADE_SESSION_ID: session }, stderr: "pipe",
  });
  const client = new Client({ name: "browser-test", version: "1" });
  t.after(async () => { await requestBrowser(socket, "close", {}).catch(() => {}); await client.close(); });
  await client.connect(transport);
  const call = async arguments_ => {
    const result = await client.callTool({ name: "browser", arguments: arguments_ });
    assert.ok(!result.isError, JSON.stringify(result));
    return result;
  };
  const proxyURL = `http://127.0.0.1:${proxy.address().port}`;
  await call({ action: "open", proxy: proxyURL, url: "http://browser-test.invalid/private-token?password=do-not-log" });
  await call({ action: "open", url: "http://browser-test.invalid/second" });
  assert.ok(seen.some(url => url.endsWith("/second")), "second navigation bypassed the configured proxy");
  let debug = await requestBrowser(socket, "debug", {});
  assert.equal(debug.proxy, proxyURL);
  assert.ok(debug.browserVersion);
  assert.ok(debug.network.some(e => e.status === 403 && e.origin === "http://browser-test.invalid"));
  assert.ok(!JSON.stringify(debug).match(/private-token|password|do-not-log/));
  if (process.platform === "linux") assert.equal(debug.browsersPath, "/opt/brigade-browser");

  await call({ action: "close" });
  proxy.closeAllConnections();
  await new Promise(resolve => proxy.close(resolve));
  const failed = await client.callTool({ name: "browser", arguments: { action: "open", proxy: proxyURL, url: "http://browser-test.invalid/failed" } });
  assert.equal(failed.isError, true);
  debug = await requestBrowser(socket, "debug", {});
  assert.ok(debug.network.some(e => e.origin === "http://browser-test.invalid" && e.error === "net::ERR_PROXY_CONNECTION_FAILED"), JSON.stringify(debug));
});
