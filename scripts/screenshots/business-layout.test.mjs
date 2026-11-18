import assert from "node:assert/strict";
import test from "node:test";
import { chromium } from "playwright";

test("Business editor leaves the conversation visible during generation", async (t) => {
  const browser = await chromium.launch();
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1440, height: 720 } });
  const session = { id: "business", name: "@contact", executionPolicy: "SESSION_EXECUTION_POLICY_ON_DEMAND", kind: "SESSION_KIND_ACP" };
  const messages = Array.from({ length: 40 }, (_, i) => ({
    id: String(i), author: "contact", delivery: "received", content: "Реплика " + i,
    createdAt: String(Math.floor(Date.now() / 1000)), source: "telegram-business/bot/conn/1",
  }));
  const rpc = {
    Me: { id: "viewer", username: "viewer" }, List: { sessions: [session] }, Get: { session },
    GetHistory: { messages }, GetDraftRun: { runId: "generation", status: "running" },
    ListBots: { bots: [{ id: "bot", businessCanReply: true }] },
  };
  await page.route("**/brigade.v1.*/*", (route) =>
    route.fulfill({ json: rpc[new URL(route.request().url()).pathname.split("/").pop()] ?? {} }));
  await page.goto(new URL("/s/business", process.env.BRIGADE_URL || "http://localhost:8787").href);
  const history = page.getByRole("main", { name: "Переписка" });
  const editor = page.getByRole("complementary", { name: "Подготовка ответа" });
  await editor.getByText("Агент готовит черновик").waitFor();
  const chat = await history.boundingBox();
  const panel = await editor.boundingBox();
  assert.ok(chat.x + chat.width <= panel.x && chat.height >= 0.8 * panel.height);
  const input = await editor.getByRole("textbox", { name: "Инструкция для черновика" }).boundingBox();
  const model = await editor.getByRole("button", { name: /Модель:/ }).boundingBox();
  assert.ok(model.y >= input.y + input.height);
  await page.setViewportSize({ width: 390, height: 740 });
  const mobileChat = await history.boundingBox();
  const mobilePanel = await editor.boundingBox();
  assert.ok(mobileChat.y + mobileChat.height <= mobilePanel.y + 1);
  assert.ok(mobileChat.height > mobilePanel.height);
  assert.ok(await editor.evaluate((el) => el.scrollWidth === el.clientWidth));
});
