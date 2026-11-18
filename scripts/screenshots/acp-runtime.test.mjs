// Требует запущенный web frontend. RPC и поток AG-UI обслуживает Playwright.
import assert from "node:assert/strict";
import test from "node:test";
import { chromium } from "playwright";

test("ACP отправляет сообщение, показывает вызовы и восстанавливает ленту после refresh", async (t) => {
  const browser = await chromium.launch();
  t.after(() => browser.close());
  const page = await browser.newPage();
  page.setDefaultTimeout(10_000);
  const session = { id: "acp-smoke", kind: "SESSION_KIND_ACP", name: "ACP smoke" };
  const prompt = "Проверь источники для smoke-теста.";
  const answer = "Проверка источников завершена.";
  const tools = ["web search", "python", "Find", "Open page"].map((toolName, i) => ({
    id: `call-${i}`, role: "tool_call", toolName, argsText: JSON.stringify({ query: `source-${i}` }), result: "ok",
  }));
  const history = [
    { id: "user-1", role: "user", content: prompt },
    ...tools,
    { id: "answer-1", role: "assistant", content: answer },
  ];
  let restore = false;
  let historyReads = 0;
  const runs = [];
  await page.route("**/brigade.v1.*/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    const rpc = {
      "/brigade.v1.AuthService/Me": { id: "viewer", username: "viewer" },
      "/brigade.v1.SessionService/List": { sessions: [session] },
      "/brigade.v1.SessionService/Get": { session },
      "/brigade.v1.AcpService/GetHistory": { messages: restore ? history : [] },
    };
    if (restore && path === "/brigade.v1.AcpService/GetHistory") historyReads++;
    return route.fulfill({ json: rpc[path] ?? {} });
  });
  await page.route("**/api/ag-ui/run", (route) => {
    const run = route.request().postDataJSON();
    runs.push(run);
    const ids = { threadId: run.threadId, runId: run.runId };
    const events = [
      { type: "RUN_STARTED", ...ids },
      ...tools.flatMap(({ id, toolName, argsText, result }) => [
        { type: "TOOL_CALL_START", toolCallId: id, toolCallName: toolName },
        { type: "TOOL_CALL_ARGS", toolCallId: id, delta: argsText },
        { type: "TOOL_CALL_END", toolCallId: id },
        { type: "TOOL_CALL_RESULT", toolCallId: id, messageId: id, role: "tool", content: result },
      ]),
      { type: "TEXT_MESSAGE_START", messageId: "answer-1", role: "assistant" },
      { type: "TEXT_MESSAGE_CONTENT", messageId: "answer-1", delta: answer },
      { type: "TEXT_MESSAGE_END", messageId: "answer-1" },
      { type: "RUN_FINISHED", ...ids },
    ];
    return route.fulfill({
      contentType: "text/event-stream",
      body: events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""),
    });
  });

  const feed = page.locator('[data-slot="aui_message-group"]');
  async function checkFeed() {
    await feed.locator('[data-role="user"]').getByText(prompt, { exact: true }).waitFor();
    await feed.locator('[data-role="assistant"]').getByText(answer, { exact: true }).waitFor();
    const activity = feed.locator("summary").filter({ hasText: "Активность · 4" });
    await activity.waitFor();
    const cards = feed.locator("details").filter({ has: page.getByText("Активность · 4", { exact: true }) });
    assert.equal(await cards.evaluate((el) => el.open), false);
    await activity.click();
    for (const { toolName } of tools) await cards.getByText(toolName, { exact: true }).waitFor();
    const args = cards.getByText("Аргументы", { exact: true });
    assert.equal(await args.count(), tools.length);
    await args.first().click();
    await cards.locator("pre").filter({ hasText: "source-0" }).waitFor();
  }

  const errors = new Set();
  page.on("pageerror", (error) => errors.add(error.stack || error.message));
  page.on("console", (message) => {
    if (/Maximum update depth exceeded|getSnapshot should be cached/i.test(message.text())) {
      errors.add(message.text());
    }
  });
  try {
    await page.goto(new URL(`/s/${session.id}`, process.env.BRIGADE_URL || "http://localhost:8787").href);
    await page.locator(".aui-composer-input").fill(prompt);
    await page.getByRole("button", { name: "Send message" }).click();
    await checkFeed();
    assert.equal(runs.length, 1);
    assert.equal(runs[0].threadId, session.id);
    assert.equal(runs[0].messages.at(-1).role, "user");
    assert.equal(runs[0].messages.at(-1).content, prompt);

    restore = true;
    await page.reload();
    await checkFeed();
    assert.ok(historyReads > 0);
    assert.equal(runs.length, 1, "Восстановление истории не должно запускать агента");
  } finally {
    assert.deepEqual([...errors], [], "Ошибки ACP runtime в Chromium");
  }
});
