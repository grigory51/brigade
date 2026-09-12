import test from "node:test";
import assert from "node:assert/strict";
import { stat } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { browserTool, requestBrowser, socketPath } from "./browser-client.mjs";

test("broker starts lazily on a private socket and stops on session close", { timeout: 10000 }, async t => {
  const previous = process.env.BRIGADE_SESSION_ID;
  const session = `browser-test-${randomUUID()}`;
  process.env.BRIGADE_SESSION_ID = session;
  const socket = socketPath(session);
  t.after(async () => {
    if (previous === undefined) delete process.env.BRIGADE_SESSION_ID;
    else process.env.BRIGADE_SESSION_ID = previous;
    await requestBrowser(socket, "close", {}).catch(() => {});
  });
  assert.deepEqual(await browserTool({ action: "ping" }), {});
  assert.equal((await stat(socket)).mode & 0o777, 0o600);
  assert.deepEqual(await requestBrowser(socket, "user", { action: "status", requestId: "stale" }), { state: "expired", requestId: "stale" });
  await requestBrowser(socket, "close", {});
  for (let i = 0; i < 50; i++) {
    try { await requestBrowser(socket, "agent", { action: "ping" }); }
    catch (error) {
      if (["ENOENT", "ECONNREFUSED"].includes(error.code)) return;
      assert.equal(error.code, "ECONNRESET");
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.fail("Browser broker still accepts requests after session close");
});
