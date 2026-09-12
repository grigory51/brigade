import test from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { chromium } from "playwright";
import { SessionBrowser } from "./browser-server.mjs";

test("real Chromium: manual login, pointer drag, cookies and same-tab continuation", { skip: process.env.BRIGADE_BROWSER_SMOKE !== "1", timeout: 60000 }, async t => {
  const server = http.createServer((req, res) => {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    if (req.url === "/login" && req.method === "POST") {
      res.writeHead(302, { "Set-Cookie": "test_session=authorized; HttpOnly; SameSite=Lax", Location: "/account" }); res.end(); return;
    }
    if (req.url === "/account") { res.end(req.headers.cookie?.includes("test_session=authorized") ? "<h1>Account ready</h1>" : "<h1>Not signed in</h1>"); return; }
    res.end(`<form action="/login" method="post"><input name="password" type="password" aria-label="Password"><button>Sign in</button></form><input id="slider" type="range" style="width:300px" value="0"><output id="value">0</output><script>slider.oninput=()=>value.textContent=slider.value</script>`);
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  // BuildKit does not provide /.dockerenv; the build itself is already isolated.
  const browser = new SessionBrowser(options => chromium.launch({ ...options, ...(process.getuid() === 0 ? { chromiumSandbox: false } : {}) }));
  t.after(() => browser.close());
  const url = `http://127.0.0.1:${server.address().port}`;
  await browser.dispatch("agent", { action: "open", url });
  const page = browser.page;
  const requestId = (await browser.dispatch("agent", { action: "handoff", reason: "Войдите" })).browserRequestId;
  const manual = args => browser.dispatch("user", { requestId, ...args });
  const password = await page.locator("input[name=password]").boundingBox();
  await manual({ action: "input", input: "down", x: password.x + 10, y: password.y + 10 });
  await manual({ action: "input", input: "up", x: password.x + 10, y: password.y + 10 });
  await manual({ action: "input", input: "text", text: "test-secret" });
  assert.equal(await page.locator("input[name=password]").inputValue(), "test-secret");
  assert.ok((await manual({ action: "frame" })).image.length > 100);
  const slider = await page.locator("#slider").boundingBox();
  await manual({ action: "input", input: "down", x: slider.x + 8, y: slider.y + 8 });
  await manual({ action: "input", input: "move", x: slider.x + 230, y: slider.y + 8 });
  await manual({ action: "input", input: "up", x: slider.x + 230, y: slider.y + 8 });
  assert.ok(Number(await page.locator("#slider").inputValue()) > 50);
  const button = await page.locator("button").boundingBox();
  await manual({ action: "input", input: "down", x: button.x + 10, y: button.y + 10 });
  await manual({ action: "input", input: "up", x: button.x + 10, y: button.y + 10 });
  await page.waitForURL(`${url}/account`);
  await manual({ action: "resume" });
  assert.equal(browser.page, page);
  const result = await browser.dispatch("agent", { action: "read" });
  assert.match(result.text, /Account ready/);
  assert.ok(!JSON.stringify(result).includes("test-secret"));
  assert.ok(!JSON.stringify(result).includes("test_session"));
  await browser.dispatch("agent", { action: "open", url: `${url}/account` });
  assert.match((await browser.read()).text, /Account ready/);
});
