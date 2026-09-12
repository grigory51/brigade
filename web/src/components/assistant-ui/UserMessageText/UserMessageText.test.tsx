// @vitest-environment happy-dom
import { cleanup, fireEvent, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { UserMessageText } from "./UserMessageText";

const part = vi.hoisted(() => ({ text: "", status: { type: "complete" } }));
vi.mock("@assistant-ui/react", () => ({ useMessagePartText: () => part }));
vi.mock("@/components/assistant-ui/link-with-preview", () => ({
  LinkWithPreview: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

test.each(["\n", "\n\n", "\r\n\r\n", " \n\t\n"])("removes trailing whitespace %j from stored and live messages", (tail) => {
  part.text = `попробуй еще раз${tail}`;
  const view = render(<UserMessageText />);
  expect(view.container.textContent).toBe("попробуй еще раз");
  expect(part.text).toBe(`попробуй еще раз${tail}`);
  part.text = `другое сообщение${tail}`;
  view.rerender(<UserMessageText />);
  expect(view.container.textContent).toBe("другое сообщение");
});

test("preserves leading indentation, paragraphs and links", () => {
  const text = "    if ready:\n        run()\n\nСсылка https://example.com/page.";
  part.text = `${text}\n\n`;
  const view = render(<UserMessageText />);
  expect(view.container.textContent).toBe(text);
  expect(view.getByRole("link").getAttribute("href")).toBe("https://example.com/page");
});

test("long messages can still be expanded and collapsed", () => {
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(300);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(160);
  part.text = "строка\n".repeat(30);
  const view = render(<UserMessageText />);
  fireEvent.click(view.getByRole("button", { name: "Развернуть" }));
  expect(view.container.querySelector(".max-h-40")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Свернуть" }));
  expect(view.container.querySelector(".max-h-40")).not.toBeNull();
});
