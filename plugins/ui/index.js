import { App } from "@modelcontextprotocol/ext-apps";

export function createBrigadeApp(name, version) {
  return new App({ name, version }, {});
}
