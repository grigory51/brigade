import type { App } from "@modelcontextprotocol/ext-apps";

export declare function createBrigadeApp(name: string, version: string): App;

export declare function openBrigadeResult(app: App, result: {
  stateTool: string;
  name: string;
  revision: number;
  activate?: boolean;
}): ReturnType<App["callServerTool"]>;

export declare function getBrigadeCI(app: App, options?: { projectPath?: string }): ReturnType<App["callServerTool"]>;

export declare function appendBrigadePrompt(app: App, text: string): ReturnType<App["callServerTool"]>;
