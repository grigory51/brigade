// Требует запущенный web frontend; ответы RPC изолированы от реального сервера.
import assert from "node:assert/strict";
import test from "node:test";
import { chromium } from "playwright";

test("вложенная Business-строка помещается в узкую панель с вертикальной прокруткой", async (t) => {
  const browser = await chromium.launch();
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1024, height: 600 } });
  const handle = "@rubtsovdmv_" + "long".repeat(10);
  const preview = "Сообщение без пробелов: " + "я".repeat(100);
  const group = "Business · " + "Длинное название ".repeat(8).trimEnd();
  const sessions = Array.from({ length: 24 }, (_, i) => ({
    id: String(i), groupLabel: group, name: i === 0 ? handle : `@contact${i}`,
    kind: "SESSION_KIND_ACP", executionPolicy: "SESSION_EXECUTION_POLICY_ON_DEMAND",
    lastMessage: preview, lastMessageAt: "1731931200", unread: true, unreadCount: 12,
  }));
  await page.route("**/brigade.v1.*/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = path === "/brigade.v1.SessionService/List"
      ? { sessions }
      : path === "/brigade.v1.AuthService/Me" ? { id: "viewer", username: "viewer" } : {};
    return route.fulfill({ json });
  });
  await page.goto(new URL("/sessions", process.env.BRIGADE_URL || "http://localhost:8787").href);
  await page.addStyleTag({ content: `
    [data-slot="sidebar-wrapper"] { --sidebar-width: 224px !important; }
    [data-slot="sidebar-container"], [data-slot="sidebar-gap"] { transition: none; }
  ` });
  const title = page.getByTitle(handle, { exact: true }).and(page.locator("span"));
  const row = page.getByRole("listitem").filter({ has: title });
  const list = page.locator('[data-sidebar="group-content"]').filter({ has: title });
  await title.waitFor();
  assert.ok(await list.evaluate((el) => el.scrollHeight > el.clientHeight && el.scrollWidth === el.clientWidth));
  const right = await list.evaluate((el) => el.getBoundingClientRect().left + el.clientWidth);
  const headerBox = await page.getByTitle(group, { exact: true }).boundingBox();
  const rowBox = await row.boundingBox();
  assert.ok(rowBox.x > headerBox.x);
  assert.equal(rowBox.x + rowBox.width, headerBox.x + headerBox.width);
  assert.ok(rowBox.x + rowBox.width < right);

  const message = row.getByText(preview, { exact: true });
  for (const text of [title, message]) {
    assert.ok(await text.evaluate((el) => el.clientWidth > 0 && el.scrollWidth > el.clientWidth && getComputedStyle(el).textOverflow === "ellipsis"));
  }
  const time = row.locator("time");
  const idleTime = await time.boundingBox();
  assert.ok(idleTime?.width > 0);
  const archive = row.getByRole("button", { name: "Архивировать сессию" });
  for (const state of ["hover", "focus"]) {
    if (state === "hover") await row.hover();
    else {
      await page.mouse.move(900, 0);
      await archive.focus();
    }
    const nameBox = await title.boundingBox();
    const actionBox = await archive.boundingBox();
    assert.ok(nameBox.width > 0 && nameBox.x + nameBox.width <= actionBox.x);
    assert.equal(await archive.evaluate((el) => getComputedStyle(el).opacity), "1");
    assert.deepEqual(await time.boundingBox(), idleTime);
    assert.ok(actionBox.y + actionBox.height <= idleTime.y);
  }

  await page.locator('[data-sidebar="header"] [data-sidebar="trigger"]').click();
  await title.waitFor({ state: "hidden" });
  assert.ok(await list.evaluate((el) => el.scrollWidth === el.clientWidth));
});
