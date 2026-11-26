import { createBrigadeApp } from "@brigade/plugin-ui";
import "@brigade/plugin-ui/styles.css";
import { createGitPanel } from "./git-panel";
import { createFilesPanel } from "./files-panel";
import "./style.css";

type Result = { name: string } & (
  | { kind: "web"; url: string }
  | { kind: "terminal"; command: { argv: string[]; cwd: string } }
);
type State = { schemaVersion: number; revision: number; projectPath?: string; results: Result[] };
type Changes = {
  status: "ready" | "not_repository";
  projectPath: string;
  scope: string;
  base?: string;
  mergeBase?: string;
  files: { path: string; originalPath?: string; index: string; worktree: string; base?: string }[];
  diff: string;
  diffTruncated: boolean;
  statusTruncated: boolean;
  warnings: string[];
};
type CIJob = { id?: string; name?: string; status?: string; conclusion?: string; url?: string; stage?: string; allowFailure?: boolean };
type CIRun = CIJob & { ref?: string; jobs?: CIJob[]; jobsPartial?: boolean };
type Deployment = { id?: string; environment?: string; status?: string; url?: string; environmentUrl?: string; ref?: string; commitSha?: string };
type CI = {
  provider?: string;
  repository?: string;
  commitSha?: string;
  runs?: CIRun[];
  deployments?: Deployment[];
  partial?: boolean;
  warnings?: string[];
  fetchedAt?: string | number;
  refreshAfterSeconds?: number;
};
type Host = { openResult?: boolean; appendPrompt?: boolean; coding?: { getCI?: boolean } };
type Panel = "results" | "files" | "changes" | "ci";
type Job = { controller: AbortController; explicit: boolean };

const app = createBrigadeApp("Brigade Coder", import.meta.env.VITE_PLUGIN_VERSION || "0.0.0-dev");
const root = document.querySelector<HTMLDivElement>("#app")!;
let state: State | undefined;
let ci: CI | undefined;
let host: Host = {};
let panel: Panel = "results";
let projectPath = ".";
let connected = false;
let stopped = false;
let ciRefreshAt = 0;
const jobs = new Map<Panel, Job>();
const openedResults = new Map<string, number>();
const syncingResults = new Set<string>();

root.innerHTML = `
  <main class="coder">
    <nav class="coder-tabs" aria-label="Разделы Coder">
      <button class="brigade-plugin-button" data-tab="results" aria-current="page">Результат</button>
      <button class="brigade-plugin-button" data-tab="files">Файлы</button>
      <button class="brigade-plugin-button" data-tab="changes">Git</button>
      <button class="brigade-plugin-button" data-tab="ci">CI/CD</button>
    </nav>
    <section id="results" class="coder-panel" aria-label="Результат">
      <div class="coder-controls"><span>Именованные результаты</span><button class="brigade-plugin-button" data-refresh="results">Обновить</button><button class="brigade-plugin-button" data-cancel="results" hidden>Отмена</button></div>
      <p data-status="results" role="status">Подключение к workspace…</p><p data-error="results" role="alert" hidden></p>
      <div id="result-list"></div>
    </section>
    <section id="files" class="coder-files" aria-label="Файлы" hidden></section>
    <section id="changes" class="coder-git" aria-label="Git" hidden></section>
    <section id="ci" class="coder-panel" aria-label="CI/CD" hidden>
      <div class="coder-controls"><span>Статусы от провайдера</span><button class="brigade-plugin-button" data-refresh="ci">Обновить</button><button class="brigade-plugin-button" data-cancel="ci" hidden>Отмена</button></div>
      <p data-status="ci" role="status"></p><p data-error="ci" role="alert" hidden></p><div id="ci-list"></div>
    </section>
  </main>`;

const gitPanel = createGitPanel(byId("changes"), call);
const filesPanel = createFilesPanel(byId("files"), call);

function byId<T extends HTMLElement>(id: string): T {
  return document.getElementById(id) as T;
}

function element<K extends keyof HTMLElementTagNameMap>(tag: K, text: string, className = ""): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.textContent = text;
  node.className = className;
  return node;
}

function message(section: Panel, type: "status" | "error", text: string): void {
  const node = root.querySelector<HTMLElement>(`[data-${type}="${section}"]`)!;
  node.textContent = text;
  node.hidden = !text;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

class HostError extends Error {
  constructor(message: string, readonly reason: string, readonly retryAfterSeconds: number) { super(message); }
}

async function call<T>(name: string, args: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
  const result = await app.callServerTool({ name, arguments: args }, { signal, timeout: 30_000, maxTotalTimeout: 60_000 });
  if (result.isError) {
    const error = result.structuredContent?.error as { message?: string; reason?: string; retryAfterSeconds?: number } | undefined;
    if (error?.message) throw new HostError(error.message, error.reason || "", error.retryAfterSeconds || 0);
    const text = result.content.filter((item) => item.type === "text").map((item) => item.text).join("\n") || "Host отклонил запрос";
    let body: unknown;
    try { body = JSON.parse(text); } catch { /* Non-JSON tool errors remain unchanged. */ }
    if (body && typeof body === "object" && "error" in body && body.error && typeof body.error === "object" && "message" in body.error && typeof body.error.message === "string") {
      throw new Error(body.error.message);
    }
    throw new Error(text);
  }
  if (!result.structuredContent) throw new Error("Ответ не содержит structuredContent");
  return result.structuredContent as T;
}

function pending(section: Panel): void {
  if (section === "changes" || section === "files") return;
  const job = jobs.get(section);
  byId(section).setAttribute("aria-busy", String(Boolean(job?.explicit)));
  const refresh = root.querySelector<HTMLButtonElement>(`[data-refresh="${section}"]`)!;
  const cooling = section === "ci" && Date.now() < ciRefreshAt;
  refresh.disabled = !connected || Boolean(job) || cooling;
  refresh.title = cooling ? `Следующее обновление через ${Math.ceil((ciRefreshAt - Date.now()) / 1000)} с` : "Обновить";
  root.querySelector<HTMLButtonElement>(`[data-cancel="${section}"]`)!.hidden = !job?.explicit;
}

async function refresh(section: Panel, explicit = true): Promise<void> {
  if (!connected || stopped) return;
  if (section === "changes") { gitPanel.activate(); return; }
  if (section === "files") { filesPanel.activate(); return; }
  if (section === "ci" && Date.now() < ciRefreshAt) return;
  if (!explicit && jobs.has(section)) return;
  jobs.get(section)?.controller.abort();
  if (section === "ci" && !host.coding?.getCI) {
    message("ci", "status", "CI/CD недоступен: host не объявил интеграцию. Статусов сборки нет.");
    return;
  }
  const job: Job = { controller: new AbortController(), explicit };
  jobs.set(section, job);
  if (explicit) message(section, "status", "Загрузка…");
  pending(section);
  try {
    if (section === "results") {
      const next = await call<State>("coder.state", {}, job.controller.signal);
      if (jobs.get(section) !== job || job.controller.signal.aborted) return;
      acceptState(next);
      syncOpenedResults();
    } else {
      const project = await call<Changes>("coder.changes", { projectPath, scope: "unstaged" }, job.controller.signal);
      if (jobs.get(section) !== job || job.controller.signal.aborted) return;
      if (project.status === "not_repository") {
        ci = undefined;
        ciRefreshAt = Date.now() + 30_000;
        replaceContent("ci-list", [element("p", "В workspace ещё нет Git-проекта. Попросите агента создать проект или клонировать репозиторий; CI/CD появится после настройки.", "coder-empty")]);
        message(section, "error", "");
        message(section, "status", "");
        return;
      }
      const next = await call<CI>("brigade.coding.getCI", { projectPath }, job.controller.signal);
      if (jobs.get(section) !== job || job.controller.signal.aborted) return;
      ci = next;
      ciRefreshAt = Date.now() + Math.max(15, ci.refreshAfterSeconds || 30) * 1000;
      renderCI();
    }
    message(section, "error", "");
    message(section, "status", "");
  } catch (error) {
    if (jobs.get(section) !== job) return;
    if (job.controller.signal.aborted) {
      message(section, "status", "Загрузка отменена. Показаны ранее полученные данные, если они были.");
    } else {
      message(section, "error", errorText(error));
      message(section, "status", "");
      if (section === "ci") {
        const retry = error instanceof HostError ? error.retryAfterSeconds : 0;
        ciRefreshAt = Date.now() + Math.max(30, retry) * 1000;
        if (error instanceof HostError) {
          const prerequisites: Record<string, string> = {
            "no-remote": "CI/CD ещё не подключён: у проекта нет remote origin. Добавьте GitHub или GitLab репозиторий через агента.",
            "no-head": "В проекте ещё нет коммитов. CI/CD появится после первого коммита и его отправки в репозиторий.",
            "not-configured": "Для этого репозитория не настроен CI-провайдер или обязательный токен. Проверьте настройки инстанса и секреты MCP.",
          };
          if (prerequisites[error.reason]) {
            ci = undefined;
            replaceContent("ci-list", [element("p", prerequisites[error.reason], "coder-empty")]);
            message(section, "error", "");
          } else {
            const failures: Record<string, string> = {
              "rate-limited": "Достигнут лимит запросов CI. Обновление повторится автоматически; предыдущие данные сохранены.",
              "unsupported-workspace": "CI/CD не может безопасно прочитать Git-метаданные этого проекта. Проверьте remote и расположение .git внутри workspace.",
              "workspace-unavailable": "Не удалось прочитать Git-метаданные проекта. Проверьте доступность workspace и репозитория.",
              "credentials": "CI-провайдер отклонил доступ. Проверьте токен и его права на этот репозиторий.",
              "upstream": "CI-провайдер временно недоступен. Предыдущие данные сохранены; запрос повторится автоматически.",
              "deadline-exceeded": "CI-провайдер не ответил вовремя. Повторим запрос автоматически.",
            };
            message(section, "error", failures[error.reason] || error.message);
          }
        }
      }
    }
  } finally {
    if (jobs.get(section) === job) {
      jobs.delete(section);
      pending(section);
    }
  }
}

function acceptState(next: State): void {
  if (next.schemaVersion !== 1 || !Number.isSafeInteger(next.revision) || !Array.isArray(next.results)) {
    throw new Error("Неподдерживаемый формат Coder state");
  }
  if (state && next.revision <= state.revision) return;
  state = next;
  if ((next.projectPath || ".") !== projectPath) changeProject(next.projectPath || ".");
  renderResults();
  if (panel === "changes") gitPanel.refresh();
}

function syncOpenedResults(): void {
  if (!state || !host.openResult || stopped) return;
  const revision = state.revision;
  for (const [name, syncedRevision] of openedResults) {
    if (syncedRevision === revision || syncingResults.has(name)) continue;
    syncingResults.add(name);
    void call<{ opened: boolean }>("brigade.openResult", { stateTool: "coder.state", name, revision, activate: false })
      .then((response) => { if (response.opened) openedResults.set(name, revision); })
      .catch((error) => message("results", "error", `Не удалось обновить открытый результат: ${errorText(error)}`))
      .finally(() => syncingResults.delete(name));
  }
}

function replaceContent(id: string, nodes: Node[]): void {
  const container = byId(id);
  const scroll = byId(panel).scrollTop;
  const focused = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.focus : undefined;
  container.replaceChildren(...nodes);
  if (focused) {
    container.querySelectorAll<HTMLElement>("[data-focus]").forEach((node) => {
      if (node.dataset.focus === focused) node.focus({ preventScroll: true });
    });
  }
  byId(panel).scrollTop = scroll;
}

function renderResults(): void {
  if (!state) return;
  const nodes: HTMLElement[] = [];
  if (!state.results.length) {
    const empty = element("div", "", "coder-empty");
    empty.append(element("strong", "Начните с задачи в чате"), element("p", "Проекта ещё может не быть. Попросите агента создать его с нуля или клонировать репозиторий. Preview и terminal results появятся здесь."));
    nodes.push(empty);
  }
  for (const result of state.results) {
    const card = element("article", "", "coder-card");
    card.append(element("small", result.kind === "web" ? "WEB PREVIEW" : "TERMINAL"), element("h2", result.name));
    card.append(element("pre", result.kind === "web" ? result.url : JSON.stringify(result.command.argv, null, 2), "coder-result-value"));
    if (result.kind === "terminal") card.append(element("p", `cwd: ${result.command.cwd} · регистрация не запускает команду`, "coder-muted"));
    const button = element("button", result.kind === "web" ? "Открыть preview" : "Открыть терминал", "brigade-plugin-button");
    button.type = "button";
    button.dataset.focus = result.name;
    button.disabled = !host.openResult || !connected;
    const notice = element("p", host.openResult ? "" : "Открытие результатов требует поддержки host.", "coder-muted");
    notice.setAttribute("role", "status");
    button.addEventListener("click", async () => {
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
      notice.textContent = "Открываю в host…";
      try {
        const revision = state!.revision;
        const response = await call<{ opened: boolean }>("brigade.openResult", { stateTool: "coder.state", name: result.name, revision, activate: true });
        if (!response.opened) throw new Error("Host не подтвердил открытие результата");
        openedResults.set(result.name, revision);
        syncOpenedResults();
        notice.textContent = "Открыто в host. Запуск terminal command подтверждается отдельно.";
      } catch (error) {
        notice.textContent = errorText(error);
      } finally {
        button.disabled = !host.openResult || !connected;
        button.removeAttribute("aria-busy");
      }
    });
    card.append(button, notice);
    nodes.push(card);
  }
  replaceContent("result-list", nodes);
}

function safeLink(url: string | undefined, label: string): HTMLElement | undefined {
  if (!url) return undefined;
  try {
    const parsed = new URL(url);
    if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password) return undefined;
  } catch { return undefined; }
  const button = element("button", label, "brigade-plugin-button");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    try {
      const response = await app.openLink({ url });
      if (response.isError) throw new Error("Host отклонил ссылку");
    } catch (error) {
      message("ci", "error", errorText(error));
    } finally {
      button.disabled = false;
      button.removeAttribute("aria-busy");
    }
  });
  return button;
}

function renderCI(): void {
  if (!ci) return;
  const nodes: HTMLElement[] = [];
  nodes.push(element("p", `${ci.provider || "Провайдер не указан"} · ${ci.repository || "Репозиторий не указан"} · ${ci.commitSha || "SHA не указан"}`, "coder-muted"));
  if (ci.fetchedAt) nodes.push(element("p", `Получено: ${new Date(Number(ci.fetchedAt)).toLocaleString()}`, "coder-muted"));
  if (ci.partial) nodes.push(element("p", "Данные неполные. Это не означает успешную сборку.", "coder-warning"));
  for (const warning of ci.warnings || []) nodes.push(element("p", warning, "coder-warning"));
  for (const run of ci.runs || []) {
    const card = element("article", "", "coder-card");
    card.append(element("h2", run.name || run.id || "Pipeline"), element("p", [run.status || "Статус не указан", run.conclusion, run.ref].filter(Boolean).join(" · ")));
    for (const job of run.jobs || []) {
      const row = element("div", `${job.stage ? `${job.stage} / ` : ""}${job.name || job.id || "Job"}: ${job.status || "Статус не указан"}${job.conclusion ? ` / ${job.conclusion}` : ""}${job.allowFailure ? " · allow failure" : ""}`, "coder-job");
      const link = safeLink(job.url, "Логи");
      if (link) row.append(link);
      card.append(row);
    }
    if (run.jobsPartial) card.append(element("p", "Список jobs неполный", "coder-warning"));
    const link = safeLink(run.url, "Открыть pipeline");
    if (link) card.append(link);
    nodes.push(card);
  }
  if (!(ci.runs || []).length) nodes.push(element("p", "Провайдер не вернул сборок для этого commit. Это не успешный CI."));
  nodes.push(element("h2", "Deployments"));
  for (const deployment of ci.deployments || []) {
    const card = element("article", "", "coder-card");
    card.append(element("h3", deployment.environment || deployment.id || "Deployment"), element("p", `${deployment.status || "Статус не указан"} · ${deployment.ref || deployment.commitSha || "Ref не указан"}`));
    for (const link of [safeLink(deployment.url, "Логи deployment"), safeLink(deployment.environmentUrl, "Открыть environment")]) if (link) card.append(link);
    nodes.push(card);
  }
  if (!(ci.deployments || []).length) nodes.push(element("p", "Провайдер не вернул deployments."));
  replaceContent("ci-list", nodes);
}

root.querySelectorAll<HTMLButtonElement>("[data-tab]").forEach((button) => {
  button.addEventListener("click", () => {
    panel = button.dataset.tab as Panel;
    for (const name of ["results", "files", "changes", "ci"] as const) byId(name).hidden = name !== panel;
    root.querySelectorAll<HTMLElement>("[data-tab]").forEach((tab) => {
      if (tab.dataset.tab === panel) tab.setAttribute("aria-current", "page");
      else tab.removeAttribute("aria-current");
    });
    if (panel === "changes") gitPanel.activate();
    else if (panel === "files") filesPanel.activate();
    else if ((panel === "results" && !state) || (panel === "ci" && !ci)) void refresh(panel);
  });
});
root.querySelectorAll<HTMLButtonElement>('[data-refresh="results"], [data-refresh="ci"]').forEach((button) => button.addEventListener("click", () => void refresh(button.dataset.refresh as Panel)));
root.querySelectorAll<HTMLButtonElement>("[data-cancel]").forEach((button) => button.addEventListener("click", () => jobs.get(button.dataset.cancel as Panel)?.controller.abort()));
function changeProject(next: string): void {
  projectPath = next;
  gitPanel.setProject(next);
  filesPanel.setProject(next);
  for (const name of ["ci"] as const) {
    jobs.get(name)?.controller.abort();
    jobs.delete(name);
    message(name, "error", "");
    message(name, "status", "Данные нового проекта ещё не загружены.");
    pending(name);
  }
  ci = undefined;
  ciRefreshAt = 0;
  byId("ci-list").replaceChildren();
  if (panel === "ci") void refresh(panel);
}

app.ontoolresult = (result) => {
  try {
    const data = result.structuredContent;
    if (!data || result.isError) return;
    if (data.brigadeHost && typeof data.brigadeHost === "object") {
      const next = data.brigadeHost as Host;
      if (JSON.stringify([host.openResult, host.appendPrompt, host.coding]) !== JSON.stringify([next.openResult, next.appendPrompt, next.coding])) {
        host = next;
        renderResults();
        gitPanel.setCanAppend(Boolean(host.appendPrompt));
        if (panel === "ci" && !ci) void refresh("ci");
      }
    } else if (data.schemaVersion === 1) {
      acceptState(data as State);
      syncOpenedResults();
    }
  } catch (error) { message("results", "error", errorText(error)); }
};

const polling = window.setInterval(() => {
  if (!connected || stopped || document.hidden) return;
  pending("ci");
  void refresh("results", false);
  if (panel === "ci" && host.coding?.getCI && Date.now() >= ciRefreshAt) void refresh("ci", false);
}, 3000);

window.addEventListener("pagehide", () => {
  stopped = true;
  window.clearInterval(polling);
  jobs.forEach((job) => job.controller.abort());
  gitPanel.close();
  filesPanel.close();
  void app.close();
});

for (const name of ["results", "changes", "ci"] as const) pending(name);

void app.connect().then(() => {
  connected = true;
  gitPanel.connect();
  filesPanel.connect();
  for (const name of ["results", "changes", "ci"] as const) pending(name);
  renderResults();
  gitPanel.setCanAppend(Boolean(host.appendPrompt));
  if (panel !== "results") void refresh(panel);
  return refresh("results");
}).catch((error) => {
  message("results", "status", "");
  message("results", "error", `Нет подключения к host: ${errorText(error)}`);
});
