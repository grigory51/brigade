import "@google/model-viewer";
import { createBrigadeApp } from "@brigade/plugin-ui";
import "@brigade/plugin-ui/styles.css";
import "./style.css";

type Parameter = {
  id: string;
  label: string;
  type: "number" | "boolean";
  value: number | boolean;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  description?: string;
};

type Check = { id: string; label: string; status: "pass" | "fail" | "warn"; detail?: string };
type Revision = { revision: number; createdAt: string; validationStatus: string };
type ModelState = {
  status?: "empty" | "building" | "ready" | "error";
  name?: string;
  revision?: number;
  source?: string;
  sourceUrl?: string;
  stepUrl?: string;
  error?: string;
  parameters?: Parameter[];
  revisions?: Revision[];
  validation?: {
    status?: string;
    checks?: Check[];
    bounds?: { x: number; y: number; z: number; unit: string };
    solidCount?: number;
    volume?: number;
  };
  pipeline?: { stage?: string; status?: string };
};
type PreviewState = { status?: string; mimeType?: string; data?: string };
type HostState = { generating?: boolean; lastMessage?: string };
type Panel = "parameters" | "validation" | "revisions" | "source";

const root = document.querySelector<HTMLDivElement>("#app")!;
const app = createBrigadeApp("Brigade CAD", import.meta.env.VITE_PLUGIN_VERSION);
let state: ModelState = { status: "empty", parameters: [], revisions: [] };
let host: HostState = {};
let panel: Panel = "parameters";
let previewUrl = "";
let previewRevision = 0;
let stateSignature = "";
let busy = false;
let error = "";

function escapeHtml(value: unknown): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function number(value: number | undefined, digits = 1): string {
  return typeof value === "number" ? value.toLocaleString(undefined, { maximumFractionDigits: digits }) : "—";
}

function statusLabel(): string {
  if (busy || state.status === "building") return "Building";
  if (state.status === "error") return "Build failed";
  if (state.status === "ready" && state.validation?.status === "pass") return "Validated";
  if (state.status === "ready") return "Needs review";
  return "No model";
}

function render() {
  const bounds = state.validation?.bounds;
  const hasModel = Boolean(previewUrl && state.revision);
  root.innerHTML = `
    <main class="cad-workbench">
      <header class="cad-header">
        <div class="cad-title">
          <span class="cad-mark" aria-hidden="true"></span>
          <div>
            <strong>${escapeHtml(state.name || "Untitled part")}</strong>
            <span>STEP-first · build123d${state.revision ? ` · revision ${state.revision}` : ""}</span>
          </div>
        </div>
        <div class="cad-header-actions">
          <span class="cad-status cad-status--${escapeHtml(state.status || "empty")}">${statusLabel()}</span>
          <button class="cad-action" data-download="source" ${state.sourceUrl ? "" : "disabled"}>Python</button>
          <button class="cad-action cad-action--primary" data-download="step" ${state.stepUrl ? "" : "disabled"}>Export STEP</button>
        </div>
      </header>

      <section class="cad-stage">
        <div class="cad-viewport">
          <div class="cad-axis" aria-hidden="true"><i></i><b>X</b><b>Y</b></div>
          ${hasModel ? `<model-viewer src="${previewUrl}" camera-controls shadow-intensity="1" environment-image="neutral" interaction-prompt="none"></model-viewer>` : `
            <div class="cad-empty">
              <span class="cad-empty-shape" aria-hidden="true"></span>
              <strong>Describe the part to begin</strong>
              <p>Try “Create a 60 × 40 mm mounting plate with four M4 holes.”</p>
            </div>`}
          ${state.status === "building" || busy ? `<div class="cad-building"><span></span>Rebuilding geometry</div>` : ""}
          ${state.status === "error" ? `<div class="cad-error"><strong>Build failed</strong><span>${escapeHtml(state.error)}</span></div>` : ""}
          ${bounds ? `<div class="cad-dimensions"><span>X ${number(bounds.x)} ${escapeHtml(bounds.unit)}</span><span>Y ${number(bounds.y)} ${escapeHtml(bounds.unit)}</span><span>Z ${number(bounds.z)} ${escapeHtml(bounds.unit)}</span></div>` : ""}
        </div>

        <aside class="cad-inspector">
          <nav class="cad-tabs" aria-label="CAD workspace panels">
            ${tab("parameters", "Parameters", state.parameters?.length || 0)}
            ${tab("validation", "Checks", state.validation?.checks?.length || 0)}
            ${tab("revisions", "History", state.revisions?.length || 0)}
            ${tab("source", "Source")}
          </nav>
          <div class="cad-panel">${renderPanel()}</div>
        </aside>
      </section>

      <section class="cad-agent">
        <div class="cad-agent-state">
          <span class="cad-agent-dot ${host.generating ? "is-running" : ""}"></span>
          <strong>${host.generating ? "Agent is working" : "Agent"}</strong>
          <span>${escapeHtml(host.lastMessage || "Ready for the next instruction")}</span>
        </div>
        <form class="cad-prompt">
          <textarea rows="1" placeholder="Describe a part or ask for a change…" aria-label="CAD instruction"></textarea>
          <button type="submit" ${host.generating ? "disabled" : ""} aria-label="Send instruction">↑</button>
        </form>
        ${error ? `<div class="cad-toast">${escapeHtml(error)}</div>` : ""}
      </section>
    </main>`;
  bind();
}

function tab(id: Panel, label: string, count?: number): string {
  return `<button class="${panel === id ? "is-active" : ""}" data-panel="${id}">${label}${count ? `<small>${count}</small>` : ""}</button>`;
}

function renderPanel(): string {
  if (panel === "parameters") {
    const parameters = state.parameters || [];
    if (!parameters.length) return `<div class="cad-panel-empty"><strong>No editable parameters</strong><span>The next agent build can expose dimensions here.</span></div>`;
    return `<form class="cad-parameters">
      ${parameters.map((parameter) => parameter.type === "boolean" ? `
        <label class="cad-toggle">
          <span><strong>${escapeHtml(parameter.label)}</strong>${parameter.description ? `<small>${escapeHtml(parameter.description)}</small>` : ""}</span>
          <input name="${escapeHtml(parameter.id)}" type="checkbox" ${parameter.value ? "checked" : ""}>
        </label>` : `
        <label class="cad-parameter">
          <span><strong>${escapeHtml(parameter.label)}</strong>${parameter.description ? `<small>${escapeHtml(parameter.description)}</small>` : ""}</span>
          <div><input name="${escapeHtml(parameter.id)}" type="number" value="${escapeHtml(parameter.value)}" ${parameter.min !== undefined ? `min="${parameter.min}"` : ""} ${parameter.max !== undefined ? `max="${parameter.max}"` : ""} step="${parameter.step ?? "any"}"><em>${escapeHtml(parameter.unit || "")}</em></div>
        </label>`).join("")}
      <button class="cad-panel-action" type="submit">Rebuild with parameters</button>
    </form>`;
  }
  if (panel === "validation") {
    const checks = state.validation?.checks || [];
    return `<div class="cad-checks">
      <div class="cad-metrics">
        <div><span>Solids</span><strong>${number(state.validation?.solidCount, 0)}</strong></div>
        <div><span>Volume</span><strong>${number(state.validation?.volume)} <small>mm³</small></strong></div>
      </div>
      ${checks.map((check) => `<div class="cad-check cad-check--${check.status}"><i>${check.status === "pass" ? "✓" : "!"}</i><span><strong>${escapeHtml(check.label)}</strong><small>${escapeHtml(check.detail)}</small></span></div>`).join("") || `<div class="cad-panel-empty"><strong>Not checked yet</strong><span>Checks run after the first successful build.</span></div>`}
      ${checks.some((check) => check.status === "fail") ? `<button class="cad-panel-action" data-repair>Ask agent to repair</button>` : ""}
    </div>`;
  }
  if (panel === "revisions") {
    const revisions = state.revisions || [];
    return `<div class="cad-revisions">${revisions.map((revision, index) => `
      <div class="cad-revision ${index === 0 ? "is-current" : ""}">
        <i></i><span><strong>Revision ${revision.revision}</strong><small>${new Date(revision.createdAt).toLocaleString()}</small></span>
        ${index === 0 ? `<em>Current</em>` : `<button data-restore="${revision.revision}">Restore</button>`}
      </div>`).join("") || `<div class="cad-panel-empty"><strong>No revisions</strong><span>Every successful build is saved here.</span></div>`}</div>`;
  }
  return `<form class="cad-source"><textarea spellcheck="false" aria-label="build123d source">${escapeHtml(state.source || "# The generated build123d source will appear here")}</textarea><button class="cad-panel-action" type="submit" ${state.source ? "" : "disabled"}>Rebuild source</button></form>`;
}

function bind() {
  root.querySelectorAll<HTMLButtonElement>("[data-panel]").forEach((button) => {
    button.addEventListener("click", () => { panel = button.dataset.panel as Panel; render(); });
  });
  root.querySelector<HTMLButtonElement>('[data-download="source"]')?.addEventListener("click", () => {
    if (state.sourceUrl) void download(state.sourceUrl, `${state.name}.py`, "text/x-python");
  });
  root.querySelector<HTMLButtonElement>('[data-download="step"]')?.addEventListener("click", () => {
    if (state.stepUrl) void download(state.stepUrl, `${state.name}.step`, "model/step");
  });
  root.querySelector<HTMLFormElement>(".cad-prompt")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const textarea = event.currentTarget.querySelector("textarea")!;
    const text = textarea.value.trim();
    if (!text) return;
    textarea.value = "";
    host.generating = true;
    updateHost();
    try {
      const result = await app.sendMessage({ role: "user", content: [{ type: "text", text }] });
      if (result.isError) throw new Error("The host rejected the instruction");
    } catch (reason) {
      host.generating = false;
      showError(reason);
    }
  });
  root.querySelector<HTMLFormElement>(".cad-parameters")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const values: Record<string, number | boolean> = {};
    for (const parameter of state.parameters || []) {
      const input = event.currentTarget.elements.namedItem(parameter.id) as HTMLInputElement;
      values[parameter.id] = parameter.type === "boolean" ? input.checked : input.valueAsNumber;
    }
    await callAndApply("cad.update_parameters", { values });
  });
  root.querySelector<HTMLFormElement>(".cad-source")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const source = event.currentTarget.querySelector("textarea")!.value;
    await callAndApply("cad.rebuild", { source });
  });
  root.querySelector<HTMLButtonElement>("[data-repair]")?.addEventListener("click", async () => {
    const failed = state.validation?.checks?.filter((check) => check.status === "fail").map((check) => `${check.label}: ${check.detail}`).join("; ");
    await app.sendMessage({ role: "user", content: [{ type: "text", text: `Repair the current CAD model using these validation failures: ${failed}` }] });
  });
  root.querySelectorAll<HTMLButtonElement>("[data-restore]").forEach((button) => {
    button.addEventListener("click", () => void callAndApply("cad.restore", { revision: Number(button.dataset.restore) }));
  });
}

async function callAndApply(name: string, args: Record<string, unknown>) {
  busy = true;
  error = "";
  render();
  try {
    const result = await app.callServerTool({ name, arguments: args });
    applyResult((result.structuredContent ?? {}) as Record<string, unknown>);
  } catch (reason) {
    showError(reason);
  } finally {
    busy = false;
    render();
  }
}

function showError(reason: unknown) {
  error = reason instanceof Error ? reason.message : "CAD operation failed";
  render();
  window.setTimeout(() => { error = ""; render(); }, 5000);
}

function applyResult(content: Record<string, unknown>) {
  if (content.brigadeHost && typeof content.brigadeHost === "object") {
    host = content.brigadeHost as HostState;
    updateHost();
    return;
  }
  if (typeof content.status !== "string") return;
  const next = content as ModelState;
  const signature = JSON.stringify(next);
  if (signature === stateSignature) return;
  stateSignature = signature;
  state = next;
  if (state.revision && state.revision !== previewRevision) {
    void loadPreview(state.revision);
  } else {
    render();
  }
}

function updateHost() {
  const indicator = root.querySelector<HTMLElement>(".cad-agent-dot");
  indicator?.classList.toggle("is-running", Boolean(host.generating));
  const title = root.querySelector<HTMLElement>(".cad-agent-state strong");
  if (title) title.textContent = host.generating ? "Agent is working" : "Agent";
  const message = root.querySelector<HTMLElement>(".cad-agent-state > span:last-child");
  if (message) message.textContent = host.lastMessage || "Ready for the next instruction";
  const send = root.querySelector<HTMLButtonElement>('.cad-prompt button[type="submit"]');
  if (send) send.disabled = Boolean(host.generating);
}

async function loadPreview(revision: number) {
  try {
    const result = await app.callServerTool({ name: "cad.preview", arguments: {} });
    const preview = (result.structuredContent ?? {}) as PreviewState;
    if (!preview.data) return render();
    const bytes = Uint8Array.from(atob(preview.data), (char) => char.charCodeAt(0));
    const nextUrl = URL.createObjectURL(new Blob([bytes], { type: preview.mimeType }));
    if (previewUrl) URL.revokeObjectURL(previewUrl);
    previewUrl = nextUrl;
    previewRevision = revision;
    render();
  } catch (reason) {
    showError(reason);
  }
}

function download(uri: string, name: string, mimeType: string) {
  return app.downloadFile({ contents: [{ type: "resource_link", uri, name, mimeType }] });
}

app.ontoolresult = (result) => applyResult((result.structuredContent ?? {}) as Record<string, unknown>);

async function refresh() {
  try {
    const result = await app.callServerTool({ name: "cad.open", arguments: {} });
    applyResult((result.structuredContent ?? {}) as Record<string, unknown>);
  } catch (reason) {
    showError(reason);
  } finally {
    window.setTimeout(refresh, 1500);
  }
}

render();
void app.connect().then(refresh);
