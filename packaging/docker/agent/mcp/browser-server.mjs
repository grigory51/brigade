import http from "node:http";
import { chmod, lstat, unlink } from "node:fs/promises";
import { existsSync } from "node:fs";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { socketPath, requestBrowser } from "./browser-client.mjs";

const WIDTH = 1024, HEIGHT = 720;
class BrowserError extends Error {}

export class SessionBrowser {
  constructor(launch = options => chromium.launch(options)) {
    this.launch = launch;
    this.browser = null;
    this.context = null;
    this.page = null;
    this.requestId = "";
    this.state = "expired";
    this.proxy = "";
  }

  status() {
    return { requestId: this.requestId, state: this.state, url: this.page?.url() ?? "", width: WIDTH, height: HEIGHT };
  }

  async dispatch(role, args) {
    if (args.action === "ping") return {};
    if (role === "close") { await this.close(); return {}; }
    if (role === "user") return this.manual(args);
    if (role !== "agent") throw new BrowserError("Unknown browser client");
    if (this.state === "human") throw new BrowserError("Пользователь управляет браузером. Завершите ответ и дождитесь его сообщения; не читайте страницу и не вводите данные.");
    if (args.action === "open") {
      const url = webURL(args.url);
      const proxy = args.proxy ? webURL(args.proxy) : this.proxy;
      if (this.browser && proxy !== this.proxy) throw new BrowserError("Прокси уже задан. Закройте браузер перед его сменой — текущая авторизация будет потеряна.");
      if (!this.browser) {
        const options = { headless: true, channel: "chromium", chromiumSandbox: !existsSync("/.dockerenv"), ...(proxy ? { proxy: { server: proxy } } : {}) };
        // macOS uses an installed Chrome without downloading a browser on every session start.
        if (process.platform === "darwin") options.channel = "chrome";
        try { this.browser = await this.launch(options); }
        catch { throw new BrowserError("Не удалось запустить Chromium. В Docker обновите базовый образ сессии; в macOS установите Google Chrome. В своей Linux-среде выполните playwright install --with-deps chromium из каталога brigade-mcp."); }
        this.proxy = proxy;
        this.context = await this.browser.newContext({ viewport: { width: WIDTH, height: HEIGHT }, acceptDownloads: false });
        this.context.on("page", page => {
          this.page = page;
          page.on("dialog", dialog => dialog.dismiss().catch(() => {}));
          page.on("close", () => { this.page = this.context?.pages().at(-1) ?? null; });
        });
        this.browser.on("disconnected", () => { this.browser = null; this.context = null; this.page = null; this.state = "expired"; });
        this.page = await this.context.newPage();
        this.page.setDefaultTimeout(10000);
      }
      if (!this.page) this.page = await this.context.newPage();
      this.state = "agent";
      await this.page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 });
      return this.read();
    }
    if (args.action === "close") { await this.close(); return this.status(); }
    if (!this.page) throw new BrowserError("Сначала откройте страницу инструментом browser с action=open.");
    switch (args.action) {
      case "read": return this.read();
      case "click": await this.page.locator(requiredString(args.selector, 2000)).click(); break;
      case "fill": {
        const field = this.page.locator(requiredString(args.selector, 2000));
        if ((await field.getAttribute("type"))?.toLowerCase() === "password") throw new BrowserError("Для ввода пароля передайте браузер пользователю через browser_handoff.");
        await field.fill(requiredString(args.text, 16000, true)); break;
      }
      case "press": await this.page.keyboard.press(requiredString(args.key, 100)); break;
      case "handoff":
        requiredString(args.reason, 1000);
        this.requestId = randomUUID();
        this.state = "human";
        return { browserRequestId: this.requestId, url: publicPageURL(this.page.url()), message: "Карточка браузера показана. Остановитесь и дождитесь сообщения пользователя. Не просите пароль или код в чате. После его ответа продолжите в этой же вкладке через browser/read." };
      default: throw new BrowserError("Unknown browser action");
    }
    return this.read();
  }

  async read() {
    // Password values, cookies and storage are never included in tool output.
    return { ...this.status(), url: publicPageURL(this.page.url()), title: await this.page.title(), text: (await this.page.locator("body").innerText()).slice(0, 20000) };
  }

  async manual(args) {
    if (!args.requestId || args.requestId !== this.requestId) return { state: "expired", requestId: args.requestId ?? "" };
    if (args.action === "status") return this.status();
    if (this.state !== "human") return this.status();
    if (!this.page) { this.state = "expired"; return this.status(); }
    switch (args.action) {
      case "frame": return { ...this.status(), title: await this.page.title(), image: (await this.page.screenshot({ type: "jpeg", quality: 75, timeout: 5000 })).toString("base64") };
      case "resume":
        await this.page.mouse.up();
        this.state = "agent";
        return this.status();
      case "cancel": await this.close(); this.state = "cancelled"; return this.status();
      case "input":
        switch (args.input) {
          case "move": case "down": case "up": {
            if (!Number.isFinite(args.x) || !Number.isFinite(args.y) || args.x < 0 || args.x > WIDTH || args.y < 0 || args.y > HEIGHT) throw new Error("Invalid pointer coordinates");
            await this.page.mouse.move(args.x, args.y);
            if (args.input === "down") await this.page.mouse.down();
            if (args.input === "up") await this.page.mouse.up();
            break;
          }
          case "wheel":
            if (!Number.isFinite(args.x) || !Number.isFinite(args.y) || Math.abs(args.x) > 3000 || Math.abs(args.y) > 3000) throw new Error("Invalid scroll distance");
            await this.page.mouse.wheel(args.x, args.y); break;
          case "press": await this.page.keyboard.press(requiredString(args.text, 100)); break;
          case "text": await this.page.keyboard.insertText(requiredString(args.text, 16000, true)); break;
          default: throw new Error("Unknown browser input");
        }
        return this.status();
      default: throw new Error("Unknown manual action");
    }
  }

  async close() {
    const browser = this.browser;
    this.browser = null; this.context = null; this.page = null; this.state = "expired"; this.proxy = "";
    await browser?.close();
  }
}

function requiredString(value, max, empty = false) {
  if (typeof value !== "string" || (!empty && !value.trim()) || value.length > max) throw new BrowserError("Invalid browser argument");
  return value;
}

function webURL(value) {
  const url = new URL(requiredString(value, 8192));
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new BrowserError("Используйте HTTP(S) URL без логина и пароля.");
  return url.href;
}

function publicPageURL(value) {
  const url = new URL(value);
  url.search = ""; url.hash = ""; url.username = ""; url.password = "";
  return url.href;
}

export async function serve(sessionID) {
  process.umask(0o077);
  const socket = socketPath(sessionID);
  try {
    await requestBrowser(socket, "agent", { action: "ping" });
    return;
  } catch (err) {
    if (!["ENOENT", "ECONNREFUSED"].includes(err.code)) throw err;
    if (err.code === "ECONNREFUSED") try {
      const info = await lstat(socket);
      if (!info.isSocket() || info.uid !== process.getuid()) throw new Error("Unsafe browser socket path");
      await unlink(socket);
    } catch (error) { if (error.code !== "ENOENT") throw error; }
  }
  const browser = new SessionBrowser();
  let queue = Promise.resolve();
  let lastUsed = Date.now();
  const server = http.createServer(async (req, res) => {
    res.setHeader("Content-Type", "application/json");
    res.setHeader("Cache-Control", "no-store");
    try {
      if (req.method !== "POST" || !["/agent", "/user", "/close"].includes(req.url)) throw new Error("Unknown browser endpoint");
      let body = "";
      req.setEncoding("utf8");
      for await (const chunk of req) {
        body += chunk;
        if (body.length > 65536) throw new Error("Browser request too large");
      }
      const args = JSON.parse(body);
      lastUsed = Date.now();
      const operation = queue.then(() => browser.dispatch(req.url.slice(1), args));
      queue = operation.catch(() => {});
      res.end(JSON.stringify(await operation));
      if (req.url === "/close") void stop();
    } catch (error) {
      res.statusCode = 400;
      // Playwright errors can embed typed values; only controlled messages leave the broker.
      res.end(JSON.stringify({ error: error instanceof BrowserError ? error.message : "Действие браузера не выполнено. Проверьте страницу и повторите." }));
    }
  });
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(socket, resolve); });
  await chmod(socket, 0o600);
  const stop = async () => { clearInterval(timer); server.close(); await browser.close(); };
  const timer = setInterval(() => { if (Date.now() - lastUsed > 30 * 60 * 1000) void stop(); }, 60000);
  process.once("SIGTERM", () => void stop());
  process.once("SIGINT", () => void stop());
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  serve(process.argv[2]).catch(() => { process.exitCode = 1; });
}
