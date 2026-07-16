// Playwright-снимки UI brigade → site/shots/*.png (для README и лендинга).
// Снимает три экрана: sessions (список сессий), chat (ACP-сессия), memory (дашборд памяти).
//
// Требует ЗАПУЩЕННЫЙ brigade: десктоп Brigade.app (:8787), bin/brigade или стенд.
// Первый запуск:
//   cd scripts/screenshots && npm i && npx playwright install chromium
// Снять:
//   BRIGADE_URL=http://localhost:8787 SHOT_SESSION=<acp-id> node shots.mjs
//
// Env:
//   BRIGADE_URL   базовый URL (по умолч. http://localhost:8787 — десктоп)
//   BRIGADE_USER  логин сид-юзера (по умолч. admin)
//   BRIGADE_PASS  пароль (по умолч. admin)
//   SHOT_SESSION  id ACP-сессии для chat.png (открой сессию в UI, скопируй id из URL /s/<id>).
//                 Без него chat.png снимается по первой сессии сайдбара (best-effort).

import { chromium } from "playwright";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { mkdirSync } from "node:fs";

const BASE = process.env.BRIGADE_URL || "http://localhost:8787";
const USER = process.env.BRIGADE_USER || "admin";
const PASS = process.env.BRIGADE_PASS || "admin";
const SESSION = process.env.SHOT_SESSION || "";

const here = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(here, "../../site/shots"); // scripts/screenshots → repo/site/shots
mkdirSync(OUT, { recursive: true });

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  colorScheme: "dark",
});
const page = await ctx.newPage();

async function login() {
  await page.goto(BASE, { waitUntil: "networkidle" });
  // Десктоп авто-логинит сид-юзера (cookie ставит webview); web/стенд показывают форму.
  const user = page.locator("#username");
  if (await user.count()) {
    await user.fill(USER);
    await page.locator("#password").fill(PASS);
    await page.locator('button[type="submit"]').click();
    await page.waitForURL(/\/sessions|\/s\//, { timeout: 15000 }).catch(() => {});
  }
  await wait(800);
}

async function shot(name) {
  await wait(1500); // дать UI дорисоваться (стримы/анимации)
  await page.screenshot({ path: `${OUT}/${name}.png` });
  console.log("✓", `${name}.png`);
}

await login();

// 1. Список сессий
await page.goto(`${BASE}/sessions`, { waitUntil: "networkidle" });
await shot("sessions");

// 2. ACP-сессия
if (SESSION) {
  await page.goto(`${BASE}/s/${SESSION}`, { waitUntil: "networkidle" });
} else {
  console.warn("! SHOT_SESSION не задан — chat.png по первой сессии сайдбара (best-effort)");
  const first = page.locator('[data-sidebar="menu-button"]').first();
  if (await first.count()) await first.click().catch(() => {});
}
await shot("chat");

// 3. Дашборд памяти
await page.goto(`${BASE}/memory`, { waitUntil: "networkidle" });
await shot("memory");

await browser.close();
console.log("Готово →", OUT);
