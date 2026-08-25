import { describe, expect, test } from "vitest";
import { shouldSubmitComposer, type ComposerSubmitMode } from "./composer-submit";

const key = (
  mode: ComposerSubmitMode,
  overrides: Partial<KeyboardEvent> = {},
) => shouldSubmitComposer(mode, {
  key: "Enter",
  shiftKey: false,
  metaKey: false,
  ctrlKey: false,
  isComposing: false,
  ...overrides,
});

describe("shouldSubmitComposer", () => {
  test("supports Enter and platform-modifier modes", () => {
    expect(key("enter")).toBe(true);
    expect(key("enter", { shiftKey: true })).toBe(false);
    expect(key("modifier-enter")).toBe(false);
    expect(key("modifier-enter", { metaKey: true })).toBe(true);
    expect(key("modifier-enter", { ctrlKey: true })).toBe(true);
  });
});
