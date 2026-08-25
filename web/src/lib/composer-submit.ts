export const COMPOSER_SUBMIT_MODE_KEY = "brigade.composer.submitMode";

export type ComposerSubmitMode = "enter" | "modifier-enter";

export function getComposerSubmitMode(): ComposerSubmitMode {
  return localStorage.getItem(COMPOSER_SUBMIT_MODE_KEY) === "modifier-enter"
    ? "modifier-enter"
    : "enter";
}

export function setComposerSubmitMode(mode: ComposerSubmitMode): void {
  localStorage.setItem(COMPOSER_SUBMIT_MODE_KEY, mode);
}

export function shouldSubmitComposer(
  mode: ComposerSubmitMode,
  event: Pick<KeyboardEvent, "key" | "shiftKey" | "metaKey" | "ctrlKey" | "isComposing">,
): boolean {
  if (event.key !== "Enter" || event.isComposing) return false;
  return mode === "modifier-enter"
    ? event.metaKey || event.ctrlKey
    : !event.shiftKey;
}
