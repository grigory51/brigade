// Прыжок к сообщению ленты по якорю data-nav-id: плавный скролл вьюпорта треда и
// временная подсветка сообщения. Общая точка для шкалы навигации и панели ссылок.
//
// Работаем через DOM, а не через рантайм assistant-ui: у него нет API «прокрутить к
// сообщению», а якоря уже расставлены на корнях сообщений (см. thread.tsx).

const FLASH_MS = 1400;
// Отступ сверху, чтобы сообщение не прилипало к верхней кромке вьюпорта.
const SCROLL_MARGIN = 24;

export function jumpToMessage(navId: string) {
  const target = document.querySelector<HTMLElement>(
    `[data-nav-id="${CSS.escape(navId)}"]`,
  );
  if (!target) return;

  const viewport = target.closest<HTMLElement>(
    '[data-slot="aui_thread-viewport"]',
  );
  if (viewport) {
    const delta =
      target.getBoundingClientRect().top -
      viewport.getBoundingClientRect().top -
      SCROLL_MARGIN;
    viewport.scrollTo({ top: viewport.scrollTop + delta, behavior: "smooth" });
  }

  target.classList.add("msg-flash");
  window.setTimeout(() => target.classList.remove("msg-flash"), FLASH_MS);
}
