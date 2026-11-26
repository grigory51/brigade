import "@web-git-graph/web/register";
import type { GitGraphProvider, WebGitGraphElement } from "@web-git-graph/web";
import type { GitGraphCommit, GitGraphComparison, GitGraphFileDiff, GitGraphRevision } from "@web-git-graph/protocol";
import { html as renderDiff } from "diff2html";
import { ColorSchemeType } from "diff2html/lib/types";
import "diff2html/bundles/css/diff2html.min.css";

type CallTool = <T>(name: string, args: Record<string, unknown>, signal?: AbortSignal) => Promise<T>;

export function createGitPanel(container: HTMLElement, call: CallTool) {
  container.innerHTML = `
    <p class="coder-git-status" role="status">Подключение к workspace…</p>
    <div class="coder-git-graph"></div>
    <section class="coder-git-file" aria-label="Изменения файла" hidden>
      <header><strong></strong><button class="brigade-plugin-button" data-append disabled>В запрос</button><button class="brigade-plugin-button" data-close aria-label="Закрыть diff">×</button></header>
      <p role="status" hidden></p><p role="alert" hidden></p>
      <div class="coder-git-diff"></div>
    </section>`;
  const status = container.querySelector<HTMLElement>(".coder-git-status")!;
  const graphHost = container.querySelector<HTMLElement>(".coder-git-graph")!;
  const filePanel = container.querySelector<HTMLElement>(".coder-git-file")!;
  const filename = filePanel.querySelector("strong")!;
  const diff = container.querySelector<HTMLElement>(".coder-git-diff")!;
  const fileStatus = filePanel.querySelector<HTMLElement>('[role="status"]')!;
  const fileError = filePanel.querySelector<HTMLElement>('[role="alert"]')!;
  const append = filePanel.querySelector<HTMLButtonElement>("[data-append]")!;
  let graph: WebGitGraphElement | undefined;
  let projectPath = ".";
  let connected = false;
  let active = false;
  let canAppend = false;
  let lifetime = new AbortController();
  let fileRequest: AbortController | undefined;
  let selectedCommit: GitGraphCommit | undefined;
  let comparison: GitGraphComparison | undefined;
  let fileDiff: GitGraphFileDiff | undefined;
  let reloadFile: (() => void) | undefined;
  let appendRequest: AbortController | undefined;

  const closeFile = () => {
    fileRequest?.abort();
    fileDiff = undefined;
    reloadFile = undefined;
    appendRequest?.abort();
    appendRequest = undefined;
    append.textContent = "В запрос";
    filePanel.hidden = true;
    graph?.removeAttribute("preview-open");
    container.classList.remove("has-file");
  };
  filePanel.querySelector("[data-close]")!.addEventListener("click", closeFile);

  function mount(): void {
    if (!connected || !active || graph) return;
    const current = document.createElement("web-git-graph");
    graph = current;
    current.setAttribute("hosted", "");
    current.setAttribute("details-position", "bottom");
    current.theme = "dark";
    current.columns = "date,author,commit";
    current.dateFormat = "relative";
    current.avatars = false;
    graphHost.replaceChildren(current);
    filePanel.slot = "file-preview";
    current.append(filePanel);
    const project = projectPath;
    const projectSignal = lifetime.signal;
    async function request<T>(name: string, args: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
      const combined = signal ? AbortSignal.any([projectSignal, signal]) : projectSignal;
      combined.throwIfAborted();
      const result = await call<T>(name, { ...args, projectPath: project }, combined);
      combined.throwIfAborted();
      return result;
    }
    const provider: GitGraphProvider = {
      async getCapabilities() {
        return { protocolVersion: "1", history: true, details: true, compare: true, diff: true, workingTree: true, stashes: false, maxPageSize: 100, maxDiffBytes: 262144 };
      },
      async getHistory(query = {}) {
        const page = await request<Awaited<ReturnType<GitGraphProvider["getHistory"]>>>("coder.git_history", {
          ref: query.ref, refs: query.refs, cursor: query.cursor,
          limit: Math.min(query.limit ?? 100, 100), includeWorkingTree: true,
        }, query.signal);
        if (!query.cursor) {
          status.hidden = page.commits.length > 0;
          status.textContent = page.commits.length ? "" : "Пока нет коммитов и изменений. Создайте проект или клонируйте репозиторий через агента.";
        }
        return page;
      },
      getCommitDetails: (_repository, revision, signal) => request("coder.git_details", { revision }, signal),
      compare: (_repository, base, head, signal) => request("coder.git_compare", { base, head }, signal),
      getFileDiff: (_repository, base, head, path, context, signal) => request("coder.git_diff", { base, head, path, context }, signal),
    };
    current.addEventListener("gitgraph-commit-select", (event) => {
      selectedCommit = event.detail.commit;
      comparison = undefined;
      closeFile();
    });
    current.addEventListener("gitgraph-compare", (event) => {
      comparison = event.detail;
      closeFile();
    });
    current.addEventListener("gitgraph-details-close", () => {
      selectedCommit = undefined;
      comparison = undefined;
      closeFile();
    });
    current.addEventListener("gitgraph-error", () => { status.hidden = true; });
    current.addEventListener("gitgraph-refresh", () => {
      if (comparison) closeFile();
      else reloadFile?.();
      comparison = undefined;
    });
    current.addEventListener("gitgraph-file-open", (event) => {
      // The component's default viewer is a raw patch and has no root-commit diff.
      // Its public event lets the host supply a bounded, highlighted file viewer.
      event.preventDefault();
      const commit = selectedCommit;
      const head: GitGraphRevision | undefined = event.detail.head ?? comparison?.head ?? (commit && (commit.kind === "working-tree" ? { kind: "working-tree" } : { kind: "commit", oid: commit.oid }));
      if (!head) return;
      const base = head.kind === "working-tree" && !comparison ? undefined : event.detail.base ?? comparison?.base ?? (commit?.parents[0] ? { kind: "commit" as const, oid: commit.parents[0] } : undefined);
      const path = event.detail.change.path;
      closeFile();
      const load = (background = false) => {
        fileRequest?.abort();
        const job = new AbortController();
        fileRequest = job;
        fileDiff = undefined;
        filePanel.hidden = false;
        current.setAttribute("preview-open", "");
        container.classList.add("has-file");
        filename.textContent = path;
        filename.title = path;
        append.disabled = true;
        if (!background) diff.replaceChildren();
        fileStatus.textContent = background ? "Обновление diff…" : "Загрузка diff…";
        fileStatus.hidden = background;
        fileError.hidden = true;
        filePanel.setAttribute("aria-busy", "true");
        void request<GitGraphFileDiff>("coder.git_diff", { base, head, path, context: 3 }, job.signal).then((result) => {
          if (fileRequest !== job || job.signal.aborted) return;
          fileDiff = result;
          const markup = result.patch ? renderDiff(result.patch, { drawFileList: false, outputFormat: "line-by-line", colorScheme: ColorSchemeType.DARK }) : "";
          if (diff.innerHTML !== markup) diff.innerHTML = markup;
          fileStatus.textContent = result.truncated ? "Показана только часть diff: достигнут лимит размера." : result.unavailableReason ?? (result.binary ? "Бинарный файл — текстовый diff недоступен." : result.patch ? "" : "Нет текстовых изменений.");
          fileStatus.hidden = !fileStatus.textContent;
          append.disabled = !canAppend || !result.patch || Boolean(appendRequest);
        }).catch((error: unknown) => {
          if (job.signal.aborted || projectSignal.aborted) return;
          fileStatus.hidden = true;
          fileError.textContent = error instanceof Error ? error.message : String(error);
          fileError.hidden = false;
        }).finally(() => {
          if (fileRequest === job) filePanel.setAttribute("aria-busy", "false");
        });
      };
      reloadFile = () => load(true);
      load();
    });
    current.provider = provider;
  }

  append.addEventListener("click", async () => {
    if (!fileDiff?.patch || !canAppend) return;
    const patch = fileDiff;
    const request = new AbortController();
    appendRequest = request;
    const signal = AbortSignal.any([lifetime.signal, request.signal]);
    append.disabled = true;
    append.textContent = "Добавляю…";
    try {
      const result = await call<{ appended: boolean }>("brigade.appendPrompt", { text: `Проект: ${projectPath}\nФайл: ${patch.path}\n\n${patch.patch!.slice(0, 8000)}` }, signal);
      if (!result.appended) throw new Error("Host не подтвердил добавление в запрос");
      if (fileDiff === patch) { fileStatus.hidden = false; fileStatus.textContent = "Diff добавлен в поле ввода без отправки (до 8 000 символов)."; }
    } catch (error) {
      if (fileDiff === patch && !signal.aborted) { fileError.hidden = false; fileError.textContent = error instanceof Error ? error.message : String(error); }
    } finally {
      if (appendRequest !== request) return;
      appendRequest = undefined;
      append.textContent = "В запрос";
      append.disabled = !canAppend || !fileDiff?.patch;
    }
  });

  return {
    connect() { connected = true; mount(); },
    activate() { active = true; if (graph) graph.refresh(); else mount(); },
    refresh() { graph?.refresh(); },
    setCanAppend(value: boolean) { canAppend = value; append.disabled = !value || !fileDiff?.patch || Boolean(appendRequest); },
    setProject(value: string) {
      if (projectPath === value) return;
      projectPath = value;
      lifetime.abort();
      lifetime = new AbortController();
      closeFile();
      selectedCommit = undefined;
      comparison = undefined;
      graphHost.replaceChildren();
      graph = undefined;
      status.textContent = "Загрузка истории…";
      status.hidden = false;
      mount();
    },
    close() { lifetime.abort(); closeFile(); graphHost.replaceChildren(); },
  };
}
