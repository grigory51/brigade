import test from "node:test";
import assert from "node:assert/strict";
import { SessionBrowser } from "./browser-server.mjs";

function fixture() {
  const calls = [];
  const page = { url: () => "https://example.org/login", title: async () => "Login", goto: async url => calls.push(["goto", url]),
    setDefaultTimeout() {}, on() {},
    locator: () => ({ innerText: async () => "Visible page", getAttribute: async () => "text", click: async () => calls.push(["click"]), fill: async value => calls.push(["fill", value]) }),
    screenshot: async () => Buffer.from("frame"), mouse: { move: async (...args) => calls.push(["move", ...args]), down: async () => calls.push(["down"]), up: async () => calls.push(["up"]), wheel: async (...args) => calls.push(["wheel", ...args]) },
    keyboard: { press: async text => calls.push(["press", text]), insertText: async text => calls.push(["text", text]) } };
  const context = { on() {}, newPage: async () => page, pages: () => [page] };
  const browser = new SessionBrowser(async options => { calls.push(["launch", options]); return { newContext: async () => context, on() {}, close: async () => calls.push(["close"]) }; });
  return { browser, page, calls };
}

test("handoff preserves browser context and blocks all agent actions", async () => {
  const { browser, calls } = fixture();
  await browser.dispatch("agent", { action: "open", url: "https://example.org/login", proxy: "http://127.0.0.1:3129" });
  const handoff = await browser.dispatch("agent", { action: "handoff", reason: "Войдите" });
  for (const action of ["read", "open", "click", "fill", "close", "handoff"]) {
    await assert.rejects(browser.dispatch("agent", { action }), /Пользователь управляет/);
  }
  const requestId = handoff.browserRequestId;
  assert.equal((await browser.dispatch("user", { action: "frame", requestId })).image, Buffer.from("frame").toString("base64"));
  await browser.dispatch("user", { action: "input", requestId, input: "text", text: "private-password" });
  const status = await browser.dispatch("user", { action: "resume", requestId });
  assert.equal(status.state, "agent");
  assert.ok(!JSON.stringify(status).includes("private-password"));
  assert.equal((await browser.dispatch("agent", { action: "read" })).text, "Visible page");
  await browser.dispatch("agent", { action: "open", url: "https://example.org/account" });
  assert.equal(calls.filter(c => c[0] === "launch").length, 1);
  assert.equal(calls.filter(c => c[0] === "close").length, 0);
});

test("stale cards cannot operate a newer handoff; cancel closes the browser", async () => {
  const { browser, calls } = fixture();
  await browser.dispatch("agent", { action: "open", url: "https://example.org" });
  const first = (await browser.dispatch("agent", { action: "handoff", reason: "Проверка" })).browserRequestId;
  await browser.dispatch("user", { action: "resume", requestId: first });
  const second = (await browser.dispatch("agent", { action: "handoff", reason: "Вход" })).browserRequestId;
  for (const action of ["frame", "input", "resume", "cancel"]) {
    assert.equal((await browser.dispatch("user", { action, requestId: first })).state, "expired");
  }
  assert.equal(browser.state, "human");
  assert.equal((await browser.dispatch("user", { action: "cancel", requestId: second })).state, "cancelled");
  assert.equal(calls.filter(c => c[0] === "close").length, 1);
  await assert.rejects(browser.dispatch("agent", { action: "read" }), /Сначала откройте/);
});

test("rejects unsafe URLs, proxy credentials, invalid input and changed proxy", async () => {
  const { browser } = fixture();
  for (const url of ["file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.org/"]) {
    await assert.rejects(browser.dispatch("agent", { action: "open", url }), /HTTP\(S\)/);
  }
  await browser.dispatch("agent", { action: "open", url: "https://example.org", proxy: "http://localhost:3129" });
  await assert.rejects(browser.dispatch("agent", { action: "open", url: "https://example.org", proxy: "http://localhost:8080" }), /Прокси уже задан/);
  const requestId = (await browser.dispatch("agent", { action: "handoff", reason: "Вход" })).browserRequestId;
  await assert.rejects(browser.dispatch("user", { action: "input", requestId, input: "down", x: NaN, y: 0 }), /coordinates/);
  await assert.rejects(browser.dispatch("user", { action: "input", requestId, input: "evaluate", text: "document.cookie" }), /Unknown browser input/);
});

test("diagnostics are bounded and never contain URL paths, query values or input", async () => {
  const { browser } = fixture();
  for (let i = 0; i < 120; i++) {
    browser.recordNetwork({ url: () => `https://example.org/secret/${i}?token=password#otp`, method: () => "POST", resourceType: () => "fetch" }, 403);
  }
  const debug = browser.diagnostics();
  assert.equal(debug.network.length, 100);
  assert.equal(debug.network[0].origin, "https://example.org");
  assert.ok(!JSON.stringify(debug).match(/secret|token|password|otp/));
});
