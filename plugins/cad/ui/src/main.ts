import "@google/model-viewer";
import { createBrigadeApp } from "@brigade/plugin-ui";
import "@brigade/plugin-ui/styles.css";
import "./style.css";

type ModelState = {
  status?: string;
  name?: string;
  revision?: number;
  sourceUrl?: string;
  stepUrl?: string;
};

type PreviewState = { status?: string; mimeType?: string; data?: string };

const root = document.querySelector<HTMLDivElement>("#app")!;
const app = createBrigadeApp("Brigade CAD", "0.1.0");
let signature = "";
let previewUrl = "";

async function render(state: ModelState) {
  if (state.status !== "ready" || !state.revision) return;
  const nextSignature = `${state.name}:${state.revision}`;
  if (nextSignature === signature) return;
  const result = await app.callServerTool({ name: "cad.preview", arguments: {} });
  const preview = (result.structuredContent ?? {}) as PreviewState;
  if (!preview.data) return;
  const bytes = Uint8Array.from(atob(preview.data), (char) => char.charCodeAt(0));
  const nextUrl = URL.createObjectURL(new Blob([bytes], { type: preview.mimeType }));
  if (previewUrl) URL.revokeObjectURL(previewUrl);
  previewUrl = nextUrl;
  signature = nextSignature;
  root.innerHTML = `
    <model-viewer src="${previewUrl}" camera-controls shadow-intensity="1" environment-image="neutral"></model-viewer>
    <div class="brigade-plugin-toolbar cad-toolbar">
      <div><strong>${state.name ?? "Model"}</strong><span>STEP · build123d</span></div>
      <nav>
        <button class="brigade-plugin-button" data-download="source">Source</button>
        <button class="brigade-plugin-button" data-download="step">STEP</button>
      </nav>
    </div>`;
  root.querySelector('[data-download="source"]')?.addEventListener("click", () => {
    if (state.sourceUrl) void download(state.sourceUrl, `${state.name}.py`, "text/x-python");
  });
  root.querySelector('[data-download="step"]')?.addEventListener("click", () => {
    if (state.stepUrl) void download(state.stepUrl, `${state.name}.step`, "model/step");
  });
}

function download(uri: string, name: string, mimeType: string) {
  return app.downloadFile({ contents: [{ type: "resource_link", uri, name, mimeType }] });
}

app.ontoolresult = (result) => { void render((result.structuredContent ?? {}) as ModelState); };

async function refresh() {
  try {
    const result = await app.callServerTool({ name: "cad.open", arguments: {} });
    await render((result.structuredContent ?? {}) as ModelState);
  } finally {
    window.setTimeout(refresh, 1500);
  }
}
void app.connect().then(refresh);
