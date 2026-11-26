// @vitest-environment happy-dom
import { AssistantRuntimeProvider, useExternalStoreRuntime, type ThreadMessageLike } from "@assistant-ui/react";
import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Thread, type ThreadComponents } from "@/components/assistant-ui/thread";
import { TooltipProvider } from "@/components/ui/tooltip";

const identity = (message: ThreadMessageLike) => message;
const components: ThreadComponents = { ToolFallback: () => null, ToolGroup: ({ children }) => children };
function Feed({ compact }: { compact: boolean }) {
  const runtime = useExternalStoreRuntime({ messages: [], convertMessage: identity, onNew: async () => {} });
  return <TooltipProvider><AssistantRuntimeProvider runtime={runtime}><Thread compact={compact} components={components} /></AssistantRuntimeProvider></TooltipProvider>;
}
afterEach(cleanup);

test("empty compact chat keeps the composer anchored below the flexible viewport", () => {
  const view = render(<Feed compact />);
  const root = view.container.querySelector(".aui-thread-root")!;
  const viewport = view.container.querySelector('[data-slot="aui_thread-viewport"]')!;
  const footer = view.container.querySelector(".aui-thread-viewport-footer")!;
  expect(root.classList.contains("min-h-0")).toBe(true);
  expect(viewport.classList.contains("min-h-0")).toBe(true);
  expect(footer.classList.contains("mt-auto")).toBe(true);
  expect(footer.classList.contains("bottom-0")).toBe(true);
});

test("plain empty chat retains its centered welcome composer", () => {
  const view = render(<Feed compact={false} />);
  const footer = view.container.querySelector(".aui-thread-viewport-footer")!;
  expect(footer.classList.contains("mt-auto")).toBe(false);
});
