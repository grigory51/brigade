import { App } from "@modelcontextprotocol/ext-apps";

export function createBrigadeApp(name, version) {
  return new App({ name, version }, {});
}

export function openBrigadeResult(app, result) {
  return app.callServerTool({ name: "brigade.openResult", arguments: result });
}

export function getBrigadeCI(app, options = {}) {
  return app.callServerTool({ name: "brigade.coding.getCI", arguments: options });
}

export function appendBrigadePrompt(app, text) {
  return app.callServerTool({ name: "brigade.appendPrompt", arguments: { text } });
}
