// Требует frontend и собранный plugins/coder/ui/dist/index.html. Все API мокируются.
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { chromium } from "playwright";

async function openSession(t, { delayedLoad = false, gitCommits = [], layout = "split", ciFailure = false, filePages = false, appKind = "coder", terminalExit, appFailure = false } = {}) {
  let html = await readFile(new URL(`../../plugins/${appKind}/ui/dist/index.html`, import.meta.url), "utf8");
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  t.after(async () => {
    if (!t.passed) for (const frame of page.frames()) t.diagnostic(await frame.locator("body").innerText().catch(() => "Frame closed"));
    await browser.close();
  });
  page.setDefaultTimeout(10_000);
  if (delayedLoad) {
    const imageURL = new URL("/smoke-delayed-image", process.env.BRIGADE_URL || "http://localhost:8787").href;
    html = html.replace("img-src data:;", `img-src data: ${new URL(imageURL).origin};`);
    const bodyEnd = html.lastIndexOf("</body>");
    html = `${html.slice(0, bodyEnd)}<img width="1" height="1" src="${imageURL}" />${html.slice(bodyEnd)}`;
    await page.route(imageURL, async (route) => {
      t.diagnostic("Delaying iframe image response by 500ms");
      await new Promise((resolve) => setTimeout(resolve, 500));
      await route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1" />' });
    });
  }
  await page.addInitScript(() => {
    if (window !== window.top) return;
    const trace = window.__bridgeTrace = [];
    const record = (event) => trace.push({ at: Math.round(performance.now()), ...event });
    const add = EventTarget.prototype.addEventListener;
    const remove = EventTarget.prototype.removeEventListener;
    EventTarget.prototype.addEventListener = function (type, ...args) {
      if (type === "message" && this === window) record({ type: "listen", stack: new Error().stack });
      return add.call(this, type, ...args);
    };
    EventTarget.prototype.removeEventListener = function (type, ...args) {
      if (type === "message" && this === window) record({ type: "unlisten" });
      return remove.call(this, type, ...args);
    };
    add.call(window, "message", (event) => {
      if (event.data?.jsonrpc === "2.0") record({ type: "message", method: event.data.method, id: event.data.id });
    });
    add.call(window, "load", (event) => {
      if (event.target instanceof HTMLIFrameElement) record({ type: "iframe-load" });
    }, true);
  });
  const session = { id: "split-smoke", kind: "SESSION_KIND_ACP", executionPolicy: "SESSION_EXECUTION_POLICY_PERSISTENT", name: "Split smoke", createdAt: String(Math.floor(Date.now() / 1000)), lastMessage: "Последний ответ агента", lastMessageAt: String(Math.floor(Date.now() / 1000)), experienceId: layout === "plain" ? "" : appKind };
  const configOptions = [
    { id: "model", name: "Model", category: "model", currentValue: "astra", options: [{ value: "astra", name: "6 Astra" }] },
    { id: "mode", name: "Mode", category: "mode", currentValue: "approve", options: [{ value: "approve", name: "Approve for me" }] },
    { id: "effort", name: "Reasoning effort", category: "thought_level", currentValue: "medium", options: [{ value: "medium", name: "Medium" }] },
  ];
  const plugin = { id: appKind, version: "1.0.0", name: appKind === "cad" ? "CAD" : "Coder", entryTool: `${appKind}.open`, layout: layout === "full" ? "full" : "split", compatible: true, configured: true };
  const state = appKind === "cad" ? { status: "empty", parameters: [], revisions: [] } : { schemaVersion: 1, revision: 1, results: [terminalExit === undefined ? { name: "Smoke preview", kind: "web", url: "https://preview.invalid/" } : { name: "Terminal output", kind: "terminal", command: { argv: ["printf", "Retained output"], cwd: "." } }] };
  const requests = { runs: 0, stateReads: 0, changes: 0, history: 0, diffs: [], ci: 0, messages: [], files: [], terminalInputs: 0, terminalClosed: false };
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route((url) => url.pathname.startsWith("/api/"), (route) => {
    requests.runs++;
    return route.abort();
  });
  await page.routeWebSocket("**/ws/**", (socket) => {
    if (terminalExit === undefined) return socket.close();
    socket.onClose(() => { requests.terminalClosed = true; });
    socket.onMessage((message) => {
      const input = JSON.parse(message.toString());
      if (input.type !== "input") return;
      requests.terminalInputs++;
      const marker = input.data.match(/777;([a-zA-Z0-9-]+);exit;/)?.[1];
      assert.ok(marker, "Команда должна содержать маркер завершения");
      socket.send(`Retained output\r\n[exit code ${terminalExit}]\r\n\x1b]777;${marker};exit;${terminalExit}\x07`);
    });
  });
  await page.route("**/brigade.v1.*/*", (route) => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/brigade.v1.SessionService/SetExperience") {
      session.experienceId = route.request().postDataJSON().experienceId || "";
      return route.fulfill({ json: { session } });
    }
    if (pathname === "/brigade.v1.SessionService/Update") {
      session.name = route.request().postDataJSON().name;
      return route.fulfill({ json: { session } });
    }
    if (pathname === "/brigade.v1.PluginService/MCP") {
      const request = route.request().postDataJSON();
      assert.deepEqual(request.expectedPlugin, { id: appKind, version: "1.0.0" });
      if (appFailure) return route.fulfill({ status: 400, json: { code: "failed_precondition", message: "Pane unavailable" } });
      const params = JSON.parse(Buffer.from(request.paramsJson, "base64").toString());
      let result;
      if (request.method === "tools/list") result = { tools: [{ name: `${appKind}.open`, inputSchema: { type: "object" }, _meta: { ui: { resourceUri: `ui://${appKind}/app.html` } } }, { name: `${appKind}.state`, inputSchema: { type: "object" }, annotations: { readOnlyHint: true } }] };
      else if (request.method === "resources/read") result = { contents: [{ uri: `ui://${appKind}/app.html`, mimeType: "text/html;profile=mcp-app", text: html }] };
      else if (params.name === "coder.git_history") {
        requests.history++;
        result = { content: [], structuredContent: { commits: gitCommits, refs: [], hasMore: false, repositoryId: "." } };
      } else if (params.name === "coder.git_details") {
        const commit = gitCommits.find((item) => item.oid === params.arguments.revision.oid || item.kind === params.arguments.revision.kind && item.kind === "working-tree");
        result = { content: [], structuredContent: { commit, refs: [], changes: [{ path: "README.md", kind: "add", staged: true, additions: 1, deletions: 0 }] } };
      } else if (params.name === "coder.git_diff") {
        requests.diffs.push(params.arguments);
        result = { content: [], structuredContent: { base: { kind: "commit", oid: "4b825dc642cb6eb9a060e54bf8d69288fbee4904" }, ...params.arguments, patch: "diff --git a/README.md b/README.md\nnew file mode 100644\n--- /dev/null\n+++ b/README.md\n@@ -0,0 +1 @@\n+Hello from first file\n" } };
      } else if (params.name === "coder.changes") {
        requests.changes++;
        result = { content: [], structuredContent: { projectPath: ".", scope: "unstaged", files: [], diff: "", warnings: [] } };
      } else {
        if (params.name === "coder.state") requests.stateReads++;
        result = { content: [], structuredContent: state };
      }
      return route.fulfill({ json: { resultJson: Buffer.from(JSON.stringify(result)).toString("base64") } });
    }
    if (pathname === "/brigade.v1.CodingService/GetCI") requests.ci++;
    if (pathname === "/brigade.v1.CodingService/GetCI" && ciFailure) return route.fulfill({ status: 400, headers: { "coding-reason": "no-remote" }, json: { code: "failed_precondition", message: "coding: no remote origin" } });
    if (pathname.startsWith("/brigade.v1.WorkspaceService/")) {
      const { path: filePath, pageToken, pageSize } = route.request().postDataJSON();
      requests.files.push({ method: pathname.split("/").pop(), path: filePath, pageToken, pageSize });
      if (pathname.endsWith("/List") && filePages) return route.fulfill({ json: {
        entries: [{ name: pageToken ? "second.md" : "first.md", path: pageToken ? "second.md" : "first.md" }],
        nextPageToken: pageToken ? "" : "500",
      } });
      if (pathname.endsWith("/List")) return route.fulfill({ json: { entries: filePath === "." ? [
        { name: "src", path: "src", directory: true },
        { name: "README.md", path: "README.md", size: "21" },
        { name: "image.bin", path: "image.bin", size: "3" },
      ] : [{ name: "main.ts", path: "src/main.ts", size: "26" }] } });
      if (pathname.endsWith("/Stat")) return route.fulfill({ json: { entry: { name: filePath, path: filePath, size: "26" } } });
      return route.fulfill({ json: { content: Buffer.from(filePath === "image.bin" ? new Uint8Array([0, 1, 2]) : filePath === "README.md" ? "# Project file viewer" : "export const answer = 42;").toString("base64") } });
    }
    const rpc = {
      "/brigade.v1.AuthService/Me": { id: "viewer", username: "viewer" },
      "/brigade.v1.AgentService/ListAgentTypes": { agentTypes: [{ id: "codex", name: "Codex", supportedKinds: ["cli", "acp"] }] },
      "/brigade.v1.AgentService/ListConnections": { connections: [{ id: "codex-main", name: "Codex", agentType: "codex" }] },
      "/brigade.v1.SessionService/List": { sessions: [session] },
      "/brigade.v1.SessionService/Get": { session },
      "/brigade.v1.PluginService/List": { plugins: [plugin] },
      "/brigade.v1.PluginService/Get": plugin,
      "/brigade.v1.AcpService/GetHistory": { messages: requests.messages, configOptions },
      "/brigade.v1.ResponseProfileService/List": { profiles: [{ id: "default", name: "Обычно" }] },
      "/brigade.v1.CodingService/GetCI": { provider: "github", repository: "test/smoke", runs: [], deployments: [], refreshAfterSeconds: 60 },
    };
    return route.fulfill({ json: rpc[pathname] ?? {} });
  });
  t.after(() => {
    assert.equal(requests.runs, 0, "Smoke не должен запускать агента или обращаться к raw API");
    assert.deepEqual(errors, [], "Ошибки браузера");
  });
  await page.goto(new URL(`/s/${session.id}`, process.env.BRIGADE_URL || "http://localhost:8787").href);
  try {
    await page.getByRole("button", { name: /^Панель:/ }).waitFor();
    await page.locator(".aui-composer-input").waitFor();
  } catch (error) {
    t.diagnostic(await page.locator("body").innerText());
    throw error;
  }
  return { page, requests };
}

test("pane failure leaves the conversation and composer available", async (t) => {
  const { page } = await openSession(t, { layout: "full", appFailure: true });
  const pane = page.getByRole("complementary", { name: "Панель сессии" });
  await pane.getByText(/Pane unavailable/).waitFor();
  const composer = page.locator(".aui-composer-input");
  await composer.fill("Черновик не должен потеряться");
  await page.getByRole("button", { name: /^Панель:/ }).click();
  await page.getByRole("menuitemradio", { name: "Без панели", exact: true }).click();
  assert.equal(await composer.inputValue(), "Черновик не должен потеряться");
  assert.equal(await pane.count(), 0);
});

test("new session chooses agent transport separately from its optional pane", async (t) => {
  const { page } = await openSession(t, { layout: "plain" });
  await page.getByRole("button", { name: /Новая сессия/ }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator('input[name="session-kind"][value="chat"]').locator("..").click();
  await dialog.getByText("Панель рядом с чатом", { exact: true }).waitFor();
  await dialog.locator('input[name="session-pane"][value="coder"]').locator("..").click();
  assert.ok(await dialog.locator('input[name="session-kind"][value="chat"]').isChecked());
  await dialog.locator('input[name="session-kind"][value="cli"]').locator("..").click();
  assert.equal(await dialog.locator('input[name="session-pane"]').count(), 0);
  await dialog.locator('input[name="session-kind"][value="chat"]').locator("..").click();
  assert.ok(await dialog.locator('input[name="session-pane"][value=""]').isChecked());
  await page.setViewportSize({ width: 390, height: 740 });
  const scrolls = await dialog.evaluate((node) => {
    const scroller = [...node.querySelectorAll("*")].find((item) => getComputedStyle(item).overflowY === "auto");
    return Boolean(scroller && scroller.clientHeight < scroller.scrollHeight);
  });
  assert.ok(scrolls, "Создание сессии должно скроллиться на телефоне");
});

test("CAD shares the native ACP chat and keeps only model controls in the app", async (t) => {
  const manifest = JSON.parse(await readFile(new URL("../../plugins/cad/manifest.json", import.meta.url), "utf8"));
  assert.equal(manifest._meta.brigade.experience.layout, "split");
  const { page } = await openSession(t, { appKind: "cad" });
  const cad = page.frameLocator('iframe[title="cad.open"]');
  await cad.getByText("Describe the part to begin", { exact: true }).waitFor();
  assert.equal(await cad.locator(".cad-agent, .cad-prompt, [data-stop]").count(), 0);
  assert.equal(await page.locator(".aui-composer-input").count(), 1);
  const composer = page.locator(".aui-composer-root");
  await composer.getByRole("button", { name: "Модель: 6 Astra" }).waitFor();
  await composer.getByRole("button", { name: "Права агента: Автоодобрение" }).waitFor();
  await composer.locator("textarea").fill("Создай монтажную пластину");
  await mkdir(".playwright-mcp/cad-review", { recursive: true });
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const viewport = await cad.locator(".cad-viewport").boundingBox();
    const input = await composer.boundingBox();
    assert.ok(viewport.width > 0 && viewport.height > 0);
    assert.ok(input.y + input.height <= 901, "Композер должен оставаться в экране");
    const fits = await cad.locator(".cad-workbench").evaluate((node) => node.scrollHeight <= node.clientHeight + 1 && document.documentElement.scrollWidth <= innerWidth);
    assert.ok(fits, "CAD не должен выходить за границы split-панели");
    const tabs = await cad.locator(".cad-tabs").evaluate((node) => {
      const buttons = [...node.querySelectorAll("button")];
      const active = node.querySelector('[aria-current="true"]');
      return {
        widths: buttons.map((button) => button.getBoundingClientRect().width),
        height: active.getBoundingClientRect().height,
        underline: getComputedStyle(active, "::after").content,
        background: getComputedStyle(active).backgroundColor,
        overflow: node.scrollWidth > node.clientWidth,
      };
    });
    assert.ok(tabs.widths.every((width) => width < 120), "Табы не должны растягиваться на всю панель");
    assert.equal(tabs.height, 32);
    assert.equal(tabs.underline, "none");
    assert.notEqual(tabs.background, "rgba(0, 0, 0, 0)");
    assert.equal(tabs.overflow, false, "Пустые табы должны помещаться в узкой панели");
    await page.screenshot({ path: `.playwright-mcp/cad-review/shared-chat-${width}.png` });
  }
  await cad.getByRole("button", { name: "Source", exact: true }).click();
  assert.equal(await cad.locator('.cad-tabs [aria-current="true"]').innerText(), "Source");
  await cad.locator(".cad-source textarea").waitFor();
  assert.equal(await composer.locator("textarea").inputValue(), "Создай монтажную пластину");
});

for (const exitCode of [0, 7]) test(`terminal exit ${exitCode} retains the result and closes its connection`, async (t) => {
  const { page, requests } = await openSession(t, { terminalExit: exitCode });
  const coder = page.frameLocator('iframe[title="coder.open"]');
  await coder.getByRole("button", { name: "Открыть терминал", exact: true }).click();
  await page.getByRole("button", { name: "Запустить", exact: true }).click();
  await page.getByText(`Команда завершена · exit code ${exitCode}`, { exact: false }).waitFor();
  await page.waitForFunction(() => document.querySelector(".xterm-rows")?.textContent.includes("Retained output"));
  assert.equal(requests.terminalInputs, 1);
  await page.getByRole("button", { name: "Приложение", exact: true }).click();
  await page.getByRole("button", { name: "Terminal output", exact: true }).click();
  assert.match(await page.locator(".xterm-rows").innerText(), /Retained output/);
  assert.equal(requests.terminalInputs, 1, "Смена вкладки не должна повторно запускать команду");
  assert.ok(requests.terminalClosed, "Соединение должно закрываться после exit");
  await page.getByRole("button", { name: "Закрыть Terminal output", exact: true }).click();
  assert.equal(await page.locator(".xterm").count(), 0);
});

for (const layout of ["plain", "split", "full"]) test(`shared session pills replace the header in ${layout} layout`, async (t) => {
  const { page } = await openSession(t, { layout });
  const controls = page.getByLabel("Инструменты сессии", { exact: true });
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const name of [/^Панель:/, "Ссылки", "MCP", "Терминал"]) {
      await controls.getByRole("button", { name, exact: typeof name === "string" }).waitFor();
    }
    const bounds = await controls.evaluate((node) => {
      const box = node.getBoundingClientRect();
      const buttons = [...node.querySelectorAll("button")].map((button) => button.getBoundingClientRect());
      const content = document.querySelector(".aui-thread-root, iframe[title='coder.open']")?.getBoundingClientRect();
      return { fits: buttons.every((button) => button.left >= 0 && button.right <= innerWidth && button.bottom <= box.bottom), bottom: box.bottom, contentTop: content?.top };
    });
    assert.ok(bounds.fits, "Пилюли должны помещаться в экран");
    if (bounds.contentTop != null) assert.ok(bounds.bottom <= bounds.contentTop + 1, "Пилюли не должны перекрывать чат или приложение");
    assert.equal(await page.locator("header").filter({ hasText: "Split smoke" }).count(), 0, "Отдельная шапка сессии удалена");
    await controls.getByRole("button", { name: "Переименовать сессию: Split smoke" }).waitFor();
    if (width === 1440) {
      const geometry = await controls.evaluate((root) => {
        const title = root.querySelector('button[aria-label^="Переименовать сессию:"]').getBoundingClientRect();
        const type = root.querySelector('button[aria-label^="Панель:"]').getBoundingClientRect();
        const style = getComputedStyle(root);
        return { titleLeft: title.left - root.getBoundingClientRect().left, gap: type.left - title.right, blur: style.backdropFilter, border: style.borderBottomWidth, after: getComputedStyle(root, "::after").content };
      });
      assert.ok(geometry.titleLeft <= 20, "Заголовок должен быть у левого края контента");
      assert.ok(geometry.gap >= 0 && geometry.gap <= 12, "Пилюли должны идти сразу после заголовка");
      assert.equal(geometry.blur, "none", "Шапка не должна размывать фон");
      assert.equal(geometry.border, "1px", "Шапка должна отделяться тонкой нижней границей");
      assert.ok(["none", "normal"].includes(geometry.after), "Под шапкой не должно быть glass-слоя");
    }
    assert.equal(await controls.getByRole("button", { name: /^Права агента:/ }).count(), 0);
    assert.equal(await controls.getByRole("button", { name: "Ссылки", exact: true }).innerText(), width < 640 ? "" : "Ссылки");
    assert.equal(await controls.getByRole("button", { name: "MCP", exact: true }).innerText(), width < 640 ? "" : "MCP");
    await controls.getByRole("button", { name: /^Панель:/ }).click();
    await page.getByRole("menuitemradio", { name: "Без панели", exact: true }).waitFor();
    await page.keyboard.press("Escape");
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.locator('[data-slot="sidebar-header"] [data-sidebar="trigger"]').click();
  const sidebarTrigger = page.locator('main [data-sidebar="trigger"]');
  await sidebarTrigger.waitFor();
  const triggerIsClear = await sidebarTrigger.evaluate((trigger) => {
    const button = trigger.getBoundingClientRect();
    const title = document.querySelector('button[aria-label^="Переименовать сессию:"]').getBoundingClientRect();
    return title.left >= button.right;
  });
  assert.ok(triggerIsClear, "Название не должно перекрывать раскрытие сайдбара");
  await sidebarTrigger.click();
  if (layout !== "plain") {
    await controls.getByRole("button", { name: /^Панель:/ }).click();
    await page.getByRole("menuitemradio", { name: "Без панели", exact: true }).click();
    await page.locator(".aui-composer-input").waitFor();
    await controls.getByRole("button", { name: "Панель: Без панели" }).waitFor();
    assert.equal(await page.locator("iframe[title='coder.open']").count(), 0);
  }
});

test("composer settings, rename and sidebar search keep their distinct scopes", async (t) => {
  const { page } = await openSession(t, { layout: "plain" });
  const composer = page.locator(".aui-composer-root");
  await composer.getByRole("button", { name: "Модель: 6 Astra" }).waitFor();
  assert.equal(await composer.getByRole("button", { name: "Модель: 6 Astra" }).innerText(), "6 Astra");
  await composer.getByRole("button", { name: "Права агента: Автоодобрение" }).waitFor();
  assert.ok(await composer.getByRole("button", { name: "Отправить сообщение", exact: true }).isDisabled());
  const contrast = await composer.evaluate((root) => {
    const canvas = document.createElement("canvas");
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    const rgba = (color) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = color; ctx.fillRect(0, 0, 1, 1); return [...ctx.getImageData(0, 0, 1, 1).data]; };
    const luminance = (rgb) => rgb.slice(0, 3).map((value) => value / 255).map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
    const background = rgba(getComputedStyle(root.querySelector('[data-slot="aui_composer-shell"]')).backgroundColor);
    const ratio = (color) => {
      const foreground = rgba(color);
      const opaque = foreground.map((value, index) => index < 3 ? value * foreground[3] / 255 + background[index] * (1 - foreground[3] / 255) : 255);
      const first = luminance(opaque), second = luminance(background);
      return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
    };
    return {
      placeholder: ratio(getComputedStyle(root.querySelector("textarea"), "::placeholder").color),
      model: ratio(getComputedStyle(root.querySelector('button[aria-label^="Модель:"]')).color),
    };
  });
  t.diagnostic(`Composer contrast: ${JSON.stringify(contrast)}`);
  assert.ok(contrast.placeholder >= 4.5 && contrast.model >= 4.5, "Контраст текста композера должен быть не менее 4.5:1");
  assert.equal(await page.getByLabel("Навигация по диалогу", { exact: true }).count(), 0);
  await composer.getByRole("button", { name: "Настройки ответа", exact: true }).click();
  await page.getByRole("button", { name: "Стиль ответа: Обычно", exact: true }).waitFor();
  await page.getByRole("button", { name: "Глубина рассуждений: Средняя", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Переименовать сессию: Split smoke", exact: true }).click();
  await page.getByRole("textbox", { name: "Название сессии", exact: true }).fill("Новый заголовок");
  await page.getByRole("button", { name: "Сохранить название", exact: true }).click();
  await page.getByRole("button", { name: "Переименовать сессию: Новый заголовок", exact: true }).waitFor();
  assert.equal(await page.getByRole("combobox", { name: "Поиск по названию и последнему сообщению" }).count(), 0);
  await composer.locator("textarea").focus();
  await page.keyboard.press("Meta+k");
  const palette = page.getByRole("dialog", { name: "Перейти к сессии" });
  await palette.waitFor();
  const search = palette.getByRole("combobox", { name: "Поиск по названию и последнему сообщению" });
  await search.fill("Последний ответ");
  await palette.getByRole("option").filter({ hasText: "Последний ответ агента" }).waitFor();
  await search.fill("Не существует");
  await palette.getByText("Сессии не найдены.", { exact: true }).waitFor();
  await search.fill("Новый заголовок");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await palette.waitFor({ state: "hidden" });
  await page.waitForFunction(() => document.activeElement?.matches(".aui-composer-root textarea"));
  await page.keyboard.press("Control+k");
  await palette.waitFor();
  await page.waitForFunction(() => document.activeElement?.hasAttribute("cmdk-input"));
  // Radix registers the topmost dismissable layer after the opening render.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.keyboard.press("Escape");
  await palette.waitFor({ state: "hidden" });
  await page.waitForFunction(() => document.activeElement?.matches(".aui-composer-root textarea"));
  assert.ok(await composer.locator("textarea").evaluate((node) => node === document.activeElement), "Escape возвращает фокус в композер");
  assert.equal(await page.getByRole("button", { name: "Обновить список сессий", exact: true }).count(), 0);
});

test("split composer stays at the bottom for empty and long chats on desktop and mobile", async (t) => {
  const { page, requests } = await openSession(t);
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 390, height: 740 }]) {
    await page.setViewportSize(viewport);
    for (const populated of [false, true]) {
      requests.messages = populated ? Array.from({ length: 40 }, (_, index) => ({ id: String(index), role: index % 2 ? "assistant" : "user", content: `Сообщение ${index}: ${"Текст для проверки прокрутки. ".repeat(12)}` })) : [];
      await page.reload();
      await page.locator(".aui-composer-input").waitFor();
      if (populated) await page.getByText("Сообщение 39:", { exact: false }).waitFor();
      const geometry = await page.locator(".aui-thread-root").evaluate((root) => {
        const rect = (element) => { const { x, y, width, height, bottom } = element.getBoundingClientRect(); return { x, y, width, height, bottom }; };
        return { root: rect(root), composer: rect(root.querySelector(".aui-composer-root")), viewport: rect(root.querySelector('[data-slot="aui_thread-viewport"]')), app: rect(document.querySelector('aside[aria-label="Панель сессии"]')), pageHeight: document.documentElement.scrollHeight, screenHeight: window.innerHeight };
      });
      t.diagnostic(JSON.stringify({ viewport, populated, geometry }));
      if (process.env.BRIGADE_SCREENSHOT_DIR) {
        await mkdir(process.env.BRIGADE_SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.BRIGADE_SCREENSHOT_DIR, `split-${viewport.width}-${populated ? "history" : "empty"}.png`) });
      }
      assert.ok(geometry.root.bottom - geometry.composer.bottom <= 36, "Composer должен быть у нижнего края chat pane");
      assert.ok(geometry.composer.bottom <= geometry.root.bottom + 1, "Composer не должен выходить за chat pane");
      assert.ok(geometry.pageHeight <= geometry.screenHeight + 1, "История должна скроллиться внутри chat pane, а не всей страницы");
      if (populated) {
        const footersFit = await page.locator('[data-slot="aui_assistant-message-root"]').evaluateAll((messages) =>
          messages.length > 0 && messages.every((message) => {
            const footer = message.querySelector('[data-slot="aui_assistant-message-footer"]');
            return footer && footer.getBoundingClientRect().bottom <= message.getBoundingClientRect().bottom + 1;
          }),
        );
        assert.ok(footersFit, "Действия должны занимать место внутри сообщения, не перекрывая следующий ответ");
      }
      if (viewport.width >= 1024) assert.ok(geometry.root.x + geometry.root.width <= geometry.app.x + 1);
      else assert.ok(geometry.root.bottom <= geometry.app.y + 1);
    }
  }
});

for (const delayedLoad of [false, true]) test(`real Coder iframe initializes and survives reload (delayed resource: ${delayedLoad})`, async (t) => {
  const { page, requests } = await openSession(t, { delayedLoad });
  for (let attempt = 0; attempt < 3; attempt++) {
    if (attempt > 0) await page.reload();
    const app = page.frameLocator('iframe[title="coder.open"]');
    try {
      await app.getByText("Smoke preview", { exact: true }).waitFor();
    } catch (error) {
      t.diagnostic(JSON.stringify(await page.evaluate(() => window.__bridgeTrace)));
      t.diagnostic(await app.locator("body").innerText());
      throw error;
    }
    await app.locator('[data-refresh="results"]:enabled').waitFor();
    await app.getByRole("button", { name: "Git", exact: true }).click();
    await app.getByText(/Пока нет коммитов и изменений/).waitFor();
    await app.getByRole("button", { name: "CI/CD", exact: true }).click();
    await app.getByText("test/smoke", { exact: false }).waitFor();
    const trace = await page.evaluate(() => window.__bridgeTrace);
    const listener = trace.findIndex((event) => event.type === "listen");
    const initialize = trace.findIndex((event) => event.method === "ui/initialize");
    assert.ok(listener >= 0 && listener < initialize, "Transport должен слушать до первого initialize");
  }
  assert.ok(requests.stateReads >= 3);
  assert.ok(requests.changes >= 3);
  assert.ok(requests.history >= 3);
  assert.ok(requests.ci >= 3);
  t.diagnostic(JSON.stringify(await page.evaluate(() => window.__bridgeTrace)));
});

for (const kind of ["working-tree", "commit"]) test(`Git graph opens a file without a parent revision: ${kind}`, async (t) => {
  const commit = { oid: kind === "working-tree" ? "__WORKTREE__" : "1".repeat(40), parents: [], message: kind === "working-tree" ? "Рабочее дерево" : "Initial commit", kind, ...(kind === "working-tree" ? { worktree: { staged: 1, unstaged: 0, untracked: 0 } } : {}) };
  const { page, requests } = await openSession(t, { gitCommits: [commit] });
  const app = page.frameLocator('iframe[title="coder.open"]');
  await app.getByRole("button", { name: "Git", exact: true }).click();
  const graph = app.locator("web-git-graph");
  await graph.locator(`.row[data-oid="${commit.oid}"]`).click();
  await graph.locator(".tree-file").filter({ hasText: "README.md" }).click();
  await app.locator(".coder-git-diff").getByText("Hello from first file", { exact: false }).waitFor();
  assert.equal(requests.diffs.length, 1);
  assert.equal(requests.diffs[0].base, undefined, "Root/unborn использует пустое дерево, а не выдуманный parent");
  assert.equal(requests.diffs[0].head.kind, kind);
  await graph.locator(".header .col-date").waitFor({ state: "hidden" });
  const layout = await graph.evaluate((node) => {
    const details = node.shadowRoot.querySelector(".inline-details").getBoundingClientRect();
    const preview = node.querySelector(".coder-git-file").getBoundingClientRect();
    return { detailsBottom: details.bottom, bottom: node.getBoundingClientRect().bottom, previewBottom: preview.bottom, detailsTop: details.top };
  });
  assert.ok(Math.abs(layout.detailsBottom - layout.bottom) <= 2, "Сведения о коммите закреплены внизу");
  assert.ok(layout.previewBottom <= layout.detailsTop + 1, "Превью файла находится над сведениями о коммите");
  if (process.env.BRIGADE_SCREENSHOT_DIR) {
    await mkdir(process.env.BRIGADE_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.BRIGADE_SCREENSHOT_DIR, `git-${kind}-desktop.png`) });
  }
  await page.setViewportSize({ width: 390, height: 740 });
  const geometry = await app.locator(".coder-git-file").evaluate((panel) => ({
    bottom: panel.getBoundingClientRect().bottom,
    viewport: window.innerHeight,
    header: panel.querySelector("header").getBoundingClientRect().height,
    height: panel.getBoundingClientRect().height,
  }));
  assert.ok(geometry.bottom <= geometry.viewport + 1, "Diff не должен выходить за app pane");
  assert.ok(geometry.height > geometry.header, "Под заголовком diff должно оставаться место для содержимого");
  const header = await graph.locator(".header").evaluate((node) => [...node.children].filter((child) => child.getBoundingClientRect().height > 0).map((child) => child.getBoundingClientRect().top));
  assert.ok(header.every((top) => top === header[0]), "Заголовки графа должны оставаться на одной строке");
  if (process.env.BRIGADE_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.BRIGADE_SCREENSHOT_DIR, `git-${kind}-mobile.png`) });
  await app.getByRole("button", { name: "Закрыть diff", exact: true }).click();
  assert.equal(await app.locator(".coder-git-file").isVisible(), false);
});

test("CI prerequisites are explained without repeated requests during cooldown", async (t) => {
  const { page, requests } = await openSession(t, { ciFailure: true });
  const app = page.frameLocator('iframe[title="coder.open"]');
  await app.getByRole("button", { name: "CI/CD", exact: true }).click();
  await app.getByText(/у проекта нет remote origin/).waitFor();
  assert.ok(await app.locator('[data-refresh="ci"]').isDisabled());
  assert.equal(await app.locator('[data-error="ci"]').isVisible(), false);
  await app.getByRole("button", { name: "Результат", exact: true }).click();
  await app.getByRole("button", { name: "CI/CD", exact: true }).click();
  assert.equal(requests.ci, 1);
});

test("Files loads folders lazily and shows text in Monaco without a terminal", async (t) => {
  const { page, requests } = await openSession(t);
  const app = page.frameLocator('iframe[title="coder.open"]');
  await app.getByText("Smoke preview", { exact: true }).waitFor();
  assert.equal(requests.files.length, 0);
  await app.getByRole("button", { name: "Файлы", exact: true }).click();
  await app.getByRole("treeitem").filter({ hasText: "README.md" }).click();
  await app.locator(".monaco-editor .view-lines").getByText("# Project file viewer", { exact: false }).waitFor();
  assert.equal(requests.files.filter(r => r.method === "List" && r.path !== ".").length, 0);
  await app.getByRole("treeitem").filter({ hasText: "src" }).click();
  await app.getByRole("treeitem").filter({ hasText: "main.ts" }).click();
  await app.locator(".monaco-editor .view-lines").getByText("export const answer = 42;", { exact: false }).waitFor();
  assert.equal(requests.files.filter(r => r.method === "List" && r.path === "src").length, 1);
  if (process.env.BRIGADE_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.BRIGADE_SCREENSHOT_DIR, "coder-files.png") });
  await app.locator(".coder-files-header").getByRole("button", { name: "Обновить" }).click();
  await app.locator(".monaco-editor .view-lines").getByText("export const answer = 42;", { exact: false }).waitFor();
  await app.getByRole("treeitem", { name: "main.ts", exact: true }).click();
  await page.keyboard.press("ArrowDown");
  assert.equal(await app.getByRole("treeitem", { name: "image.bin", exact: true }).evaluate(node => node === document.activeElement), true);
  await app.getByRole("treeitem").filter({ hasText: "image.bin" }).click();
  await app.locator(".coder-files-placeholder").getByText(/Бинарный файл/).waitFor();
  await page.setViewportSize({ width: 390, height: 740 });
  const dimensions = await app.locator(".coder-files").evaluate(node => ({ scroll: node.scrollWidth, width: node.clientWidth }));
  assert.ok(dimensions.scroll <= dimensions.width + 1, "Files должен помещаться в узкую панель");
});

test("Files loads continuation pages only on request and retains earlier files", async (t) => {
  const { page, requests } = await openSession(t, { filePages: true });
  const app = page.frameLocator('iframe[title="coder.open"]');
  await app.getByText("Smoke preview", { exact: true }).waitFor();
  await app.getByRole("button", { name: "Файлы", exact: true }).click();
  await app.getByRole("treeitem", { name: "first.md", exact: true }).waitFor();
  assert.equal(requests.files.length, 1);
  assert.equal(requests.files[0].pageSize, 500);
  await app.getByRole("treeitem", { name: "Показать ещё", exact: true }).click();
  await app.getByRole("treeitem", { name: "second.md", exact: true }).waitFor();
  await app.getByRole("treeitem", { name: "first.md", exact: true }).waitFor();
  assert.equal(requests.files.length, 2);
  assert.equal(requests.files[1].pageToken, "500");
  assert.equal(await app.getByRole("treeitem", { name: "Показать ещё", exact: true }).count(), 0);
});
