import {
  asyncDataLoaderFeature,
  createTree as createHeadlessTree,
  hotkeysCoreFeature,
  selectionFeature,
  type ItemInstance,
  type TreeInstance,
} from "@headless-tree/core";
import * as monaco from "monaco-editor/editor/editor.api.js";
import "monaco-editor/basic-languages/monaco.contribution.js";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker&inline";
import "./files-panel.css";

type CallTool = <T>(name: string, args: Record<string, unknown>, signal?: AbortSignal) => Promise<T>;
type WorkspaceEntry = { name: string; path: string; directory: boolean; symlink: boolean; size: string; modifiedAt: string };
type WorkspaceList = { entries: WorkspaceEntry[]; nextPageToken: string };
type WorkspaceRead = { text: string; binary: boolean; truncated: boolean };
type MoreEntry = { more: true; parentId: string };
type TreeEntry = WorkspaceEntry | MoreEntry | undefined;
type FileTree = TreeInstance<TreeEntry>;

const ROOT_ITEM_ID = "__brigade_workspace_root__";

(self as typeof self & { MonacoEnvironment?: { getWorker: () => Worker } }).MonacoEnvironment = {
  getWorker: () => new EditorWorker(),
};

function languageForPath(path: string): string | undefined {
  const name = path.split("/").at(-1)?.toLowerCase() ?? "";
  if (name === "dockerfile") return "dockerfile";
  const extension = name.includes(".") ? name.split(".").at(-1) : undefined;
  return {
    c: "c", cc: "cpp", cpp: "cpp", cs: "csharp", css: "css", go: "go", h: "cpp",
    htm: "html", html: "html", java: "java", js: "javascript", json: "json",
    jsx: "javascript", kt: "kotlin", kts: "kotlin", md: "markdown", py: "python",
    rs: "rust", sh: "shell", sql: "sql", svg: "xml", toml: "ini", ts: "typescript",
    tsx: "typescript", xml: "xml", yaml: "yaml", yml: "yaml",
  }[extension ?? ""];
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isMoreEntry(entry: TreeEntry): entry is MoreEntry {
  return Boolean(entry && "more" in entry);
}

export function createFilesPanel(container: HTMLElement, call: CallTool) {
  container.classList.add("coder-files");
  container.innerHTML = `
    <header class="coder-files-header"><strong>Файлы</strong><button class="brigade-plugin-button" type="button" data-refresh>Обновить</button></header>
    <div class="coder-files-body">
      <section class="coder-files-tree-pane" aria-label="Файлы проекта"><p class="coder-files-status" role="status">Подключение к workspace…</p><p class="coder-files-error" role="alert" hidden></p><button class="brigade-plugin-button" type="button" data-retry hidden>Повторить</button><div class="coder-files-tree"></div></section>
      <section class="coder-files-viewer" aria-label="Просмотр файла"><header><strong data-filename>Выберите файл</strong><span data-file-status></span></header><p class="coder-files-placeholder">Выберите текстовый файл в дереве.</p><div class="coder-files-editor" hidden></div></section>
    </div>`;

  const refresh = container.querySelector<HTMLButtonElement>("[data-refresh]")!;
  const retry = container.querySelector<HTMLButtonElement>("[data-retry]")!;
  const treePane = container.querySelector<HTMLElement>(".coder-files-tree-pane")!;
  const treeHost = container.querySelector<HTMLElement>(".coder-files-tree")!;
  const treeStatus = container.querySelector<HTMLElement>(".coder-files-status")!;
  const treeError = container.querySelector<HTMLElement>(".coder-files-error")!;
  const filename = container.querySelector<HTMLElement>("[data-filename]")!;
  const fileStatus = container.querySelector<HTMLElement>("[data-file-status]")!;
  const placeholder = container.querySelector<HTMLElement>(".coder-files-placeholder")!;
  const editorHost = container.querySelector<HTMLElement>(".coder-files-editor")!;
  let projectPath = ".";
  let connected = false;
  let active = false;
  let closed = false;
  let lifetime = new AbortController();
  let treeRequests = new AbortController();
  let fileRequest: AbortController | undefined;
  let tree: FileTree | undefined;
  let editor: monaco.editor.IStandaloneCodeEditor | undefined;
  let model: monaco.editor.ITextModel | undefined;
  let expandedItems: string[] = [];
  let selectedItems: string[] = [];
  let selectedFile: string | undefined;
  let rootPending = false;
  let rootLoaded = false;
  const listErrors = new Map<string, string>();
  const pageErrors = new Map<string, string>();
  const directoryPages = new Map<string, { entries: WorkspaceEntry[]; nextPageToken: string }>();
  const loadingPages = new Set<string>();

  function request<T>(name: string, args: Record<string, unknown>): Promise<T> {
    const signal = AbortSignal.any([lifetime.signal, treeRequests.signal]);
    signal.throwIfAborted();
    return call<T>(name, args, signal).then((result) => { signal.throwIfAborted(); return result; });
  }

  function renderTree(): void {
    if (!tree || closed) return;
    const focusedItemId = (document.activeElement as HTMLElement | null)?.dataset.treeItem;
    for (const oldItem of treeHost.querySelectorAll<HTMLElement>("[data-tree-item]")) tree.getItemInstance(oldItem.dataset.treeItem!).registerElement(null);
    treeHost.replaceChildren();
    const props = tree.getContainerProps("Файлы проекта");
    treeHost.setAttribute("role", String(props.role));
    treeHost.setAttribute("aria-label", String(props["aria-label"]));
    tree.registerElement(treeHost);
    const root = tree.getRootItem();
    const rootError = listErrors.get(ROOT_ITEM_ID);
    treeError.hidden = !rootError;
    treeError.textContent = rootError ?? "";
    retry.hidden = !rootError;
    refresh.disabled = rootPending;
    const visibleItems = tree.getItems();
    treeStatus.hidden = Boolean(rootError) || (!rootPending && !(rootLoaded && visibleItems.length === 0));
    treeStatus.textContent = rootPending ? "Загрузка файлов…" : rootLoaded ? "Папка пуста." : "";
    for (const item of visibleItems) {
      const entry = item.getItemData();
      if (!entry) continue;
      const itemProps = item.getProps();
      const row = document.createElement("button");
      row.type = "button";
      row.className = "coder-files-item";
      row.dataset.treeItem = item.getId();
      row.style.paddingInlineStart = `${12 + item.getItemMeta().level * 16}px`;
      for (const attribute of ["role", "aria-level", "aria-setsize", "aria-posinset", "aria-selected", "aria-label", "aria-expanded"] as const) {
        if (itemProps[attribute] !== undefined) row.setAttribute(attribute, String(itemProps[attribute]));
      }
      row.tabIndex = Number(itemProps.tabIndex);
      row.classList.toggle("is-selected", item.isSelected());
      row.classList.toggle("coder-files-more", isMoreEntry(entry));
      const icon = document.createElement("span");
      icon.setAttribute("aria-hidden", "true");
      icon.textContent = isMoreEntry(entry) ? "" : entry.directory ? (item.isExpanded() ? "▾" : "▸") : "·";
      const name = document.createElement("span");
      name.textContent = isMoreEntry(entry) ? (loadingPages.has(entry.parentId) ? "Загрузка…" : "Показать ещё") : entry.name;
      row.append(icon, name);
      if (!isMoreEntry(entry) && entry.symlink) { const link = document.createElement("small"); link.textContent = "ссылка"; row.append(link); }
      if (!isMoreEntry(entry) && entry.directory && item.isLoading()) { const loading = document.createElement("small"); loading.textContent = "Загрузка…"; row.append(loading); }
      if (isMoreEntry(entry)) {
        row.addEventListener("click", () => { item.setFocused(); loadNextPage(entry.parentId); });
      } else {
        row.addEventListener("click", itemProps.onClick as EventListener);
      }
      item.registerElement(row);
      treeHost.append(row);
      const itemError = isMoreEntry(entry) ? pageErrors.get(entry.parentId) : listErrors.get(item.getId());
      if (itemError && (isMoreEntry(entry) || item.isExpanded())) {
        const error = document.createElement("div");
        error.className = "coder-files-item-error";
        error.style.paddingInlineStart = `${28 + item.getItemMeta().level * 16}px`;
        const message = document.createElement("span");
        message.textContent = itemError;
        const button = document.createElement("button");
        button.type = "button";
        button.className = "brigade-plugin-button";
        button.textContent = "Повторить";
        button.addEventListener("click", () => isMoreEntry(entry) ? loadNextPage(entry.parentId) : retryDirectory(item));
        error.append(message, button);
        treeHost.append(error);
      }
    }
    if (focusedItemId) treeHost.querySelector<HTMLElement>(`[data-tree-item="${CSS.escape(focusedItemId)}"]`)?.focus();
  }

  function createTree(): void {
    tree?.setMounted(false);
    tree?.registerElement(null);
    listErrors.clear();
    pageErrors.clear();
    directoryPages.clear();
    loadingPages.clear();
    const generation = treeRequests;
    tree = createHeadlessTree<TreeEntry>({
      rootItemId: ROOT_ITEM_ID,
      initialState: { expandedItems, selectedItems },
      features: [asyncDataLoaderFeature, selectionFeature, hotkeysCoreFeature],
      dataLoader: {
        getItem: async () => undefined,
        getChildrenWithData: async (itemId) => {
          const path = itemId === ROOT_ITEM_ID ? projectPath : itemId;
          try {
            const result = await request<WorkspaceList>("brigade.workspace.list", { path });
            if (generation !== treeRequests) return [];
            listErrors.delete(itemId);
            const entries = (itemId === ROOT_ITEM_ID ? result.entries.filter((entry) => entry.name !== ".git" && entry.name !== ".brigade") : result.entries)
              .sort((left, right) => Number(right.directory) - Number(left.directory) || left.name.localeCompare(right.name));
            directoryPages.set(itemId, { entries, nextPageToken: result.nextPageToken });
            return pageChildren(itemId);
          } catch (error) {
            if (!generation.signal.aborted && !lifetime.signal.aborted) listErrors.set(itemId, errorText(error));
            return [];
          }
        },
      },
      getItemName: (item) => {
        const entry = item.getItemData();
        return isMoreEntry(entry) ? "Показать ещё" : entry?.name ?? "";
      },
      isItemFolder: (item) => {
        const entry = item.getItemData();
        return !isMoreEntry(entry) && (entry?.directory ?? false);
      },
      onPrimaryAction: (item) => {
        const entry = item.getItemData();
        if (isMoreEntry(entry)) loadNextPage(entry.parentId);
        else if (entry && !entry.directory) openFile(entry.path);
      },
      setExpandedItems: (value) => { expandedItems = typeof value === "function" ? value(expandedItems) : value; renderTree(); },
      setSelectedItems: (value) => { selectedItems = typeof value === "function" ? value(selectedItems) : value; renderTree(); },
      setState: () => renderTree(),
      scrollToItem: (item) => item.getElement()?.scrollIntoView({ block: "nearest" }),
    });
    rootPending = true;
    rootLoaded = false;
    tree.setMounted(true);
    renderTree();
    void tree.loadChildrenIds(ROOT_ITEM_ID).then(() => {
      if (generation !== treeRequests) return;
      rootPending = false;
      rootLoaded = !listErrors.has(ROOT_ITEM_ID);
      renderTree();
    });
  }

  function pageChildren(directoryId: string): Array<{ id: string; data: TreeEntry }> {
    const page = directoryPages.get(directoryId);
    if (!page) return [];
    const children: Array<{ id: string; data: TreeEntry }> = page.entries.map((entry) => ({ id: entry.path, data: entry }));
    if (page.nextPageToken) children.push({ id: `${directoryId}/__brigade_more__`, data: { more: true, parentId: directoryId } });
    return children;
  }

  function loadNextPage(directoryId: string): void {
    const page = directoryPages.get(directoryId);
    const generation = treeRequests;
    if (!page?.nextPageToken || loadingPages.has(directoryId)) return;
    const scrollTop = treePane.scrollTop;
    loadingPages.add(directoryId);
    pageErrors.delete(directoryId);
    renderTree();
    const path = directoryId === ROOT_ITEM_ID ? projectPath : directoryId;
    void request<WorkspaceList>("brigade.workspace.list", { path, pageToken: page.nextPageToken }).then((result) => {
      if (generation !== treeRequests) return;
      const entries = (directoryId === ROOT_ITEM_ID ? result.entries.filter((entry) => entry.name !== ".git" && entry.name !== ".brigade") : result.entries);
      page.entries = [...page.entries, ...entries]
        .sort((left, right) => Number(right.directory) - Number(left.directory) || left.name.localeCompare(right.name));
      page.nextPageToken = result.nextPageToken;
      const children = pageChildren(directoryId);
      for (const child of children) tree?.getItemInstance(child.id).updateCachedData(child.data, true);
      tree?.getItemInstance(directoryId).updateCachedChildrenIds(children.map((child) => child.id));
    }).catch((error) => {
      if (!generation.signal.aborted && !lifetime.signal.aborted) pageErrors.set(directoryId, errorText(error));
    }).finally(() => {
      if (generation !== treeRequests) return;
      loadingPages.delete(directoryId);
      renderTree();
      queueMicrotask(() => { treePane.scrollTop = scrollTop; });
    });
  }

  function retryDirectory(item: ItemInstance<TreeEntry>): void {
    listErrors.delete(item.getId());
    if (item.getId() === ROOT_ITEM_ID) {
      rootPending = true;
      rootLoaded = false;
      void item.invalidateChildrenIds().then(() => {
        rootPending = false;
        rootLoaded = !listErrors.has(ROOT_ITEM_ID);
        renderTree();
      });
      renderTree();
      return;
    }
    void item.invalidateChildrenIds();
    renderTree();
  }

  function ensureEditor(): monaco.editor.IStandaloneCodeEditor {
    if (!editor) editor = monaco.editor.create(editorHost, { automaticLayout: true, contextmenu: false, minimap: { enabled: false }, readOnly: true, scrollBeyondLastLine: false, theme: "vs-dark" });
    return editor;
  }

  function showPlaceholder(text: string): void {
    placeholder.textContent = text;
    placeholder.hidden = false;
    editorHost.hidden = true;
  }

  function openFile(path: string, viewState?: monaco.editor.ICodeEditorViewState | null): void {
    fileRequest?.abort();
    const currentRequest = new AbortController();
    fileRequest = currentRequest;
    selectedFile = path;
    filename.textContent = path;
    filename.title = path;
    fileStatus.textContent = "Загрузка…";
    showPlaceholder("Загрузка файла…");
    const signal = AbortSignal.any([lifetime.signal, currentRequest.signal]);
    void call<WorkspaceRead>("brigade.workspace.read", { path }, signal).then((result) => {
      signal.throwIfAborted();
      if (fileRequest !== currentRequest || selectedFile !== path) return;
      if (result.binary) { fileStatus.textContent = "Бинарный файл"; showPlaceholder("Бинарный файл нельзя показать как текст."); return; }
      model?.dispose();
      model = monaco.editor.createModel(result.text, languageForPath(path));
      const currentEditor = ensureEditor();
      currentEditor.setModel(model);
      if (viewState) currentEditor.restoreViewState(viewState);
      placeholder.hidden = true;
      editorHost.hidden = false;
      fileStatus.textContent = result.truncated ? "Показана часть файла: достигнут лимит 1 МиБ." : "";
    }).catch((error) => {
      if (signal.aborted || fileRequest !== currentRequest) return;
      fileStatus.textContent = "Ошибка загрузки";
      showPlaceholder(errorText(error));
    });
  }

  function reloadTree(): void {
    if (!connected || !active || closed) return;
    const scrollTop = treePane.scrollTop;
    const file = selectedFile;
    const viewState = editor?.saveViewState();
    treeRequests.abort();
    treeRequests = new AbortController();
    createTree();
    if (file) openFile(file, viewState);
    queueMicrotask(() => { treePane.scrollTop = scrollTop; });
  }

  refresh.addEventListener("click", reloadTree);
  retry.addEventListener("click", () => { const root = tree?.getRootItem(); if (root) retryDirectory(root); });
  return {
    connect() { connected = true; if (active) reloadTree(); },
    activate() { active = true; if (connected && !tree) reloadTree(); },
    setProject(path: string) {
      if (projectPath === path) return;
      projectPath = path;
      expandedItems = [];
      selectedItems = [];
      selectedFile = undefined;
      fileRequest?.abort();
      model?.dispose();
      model = undefined;
      filename.textContent = "Выберите файл";
      fileStatus.textContent = "";
      showPlaceholder("Выберите текстовый файл в дереве.");
      treeRequests.abort();
      treeRequests = new AbortController();
      if (connected && active) reloadTree();
    },
    close() {
      closed = true;
      lifetime.abort();
      treeRequests.abort();
      fileRequest?.abort();
      tree?.setMounted(false);
      tree?.registerElement(null);
      treeHost.replaceChildren();
      model?.dispose();
      editor?.dispose();
      model = undefined;
      editor = undefined;
    },
  };
}
