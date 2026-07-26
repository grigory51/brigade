import type { ThreadMessage } from "@assistant-ui/react";

/**
 * Извлечение ссылок из ответов агента для окна «Ссылки». Источник — сами сообщения
 * ленты: отдельного API у brigade нет, а всё, что агент показал пользователю, уже
 * лежит в тексте его реплик.
 */

export type LinkKind = "preview" | "pr" | "external";

export type SessionLink = {
  kind: LinkKind;
  label: string;
  url: string;
  // sourceId — id сообщения-источника (якорь data-nav-id для прыжка по клику).
  sourceId: string;
  // sourceIndex — порядковый номер сообщения в ленте, показывается в заголовке группы.
  sourceIndex: number;
};

// messagePlainText — плоский текст сообщения: только text-парты, без tool-call'ов и
// размышлений. Используется и для тултипов шкалы навигации.
export function messagePlainText(message: ThreadMessage): string {
  return message.content
    .filter((p): p is { type: "text"; text: string } => p.type === "text")
    .map((p) => p.text)
    .join(" ")
    .replace(/\s+/g, " ")
    .trim();
}

// Markdown-ссылка [подпись](url) и голый URL. Хвостовую пунктуацию у голого URL
// отсекаем отдельно: «см. https://example.com.» не должно давать точку в адресе.
const MD_LINK = /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g;
const BARE_URL = /https?:\/\/[^\s<>"'`)\]]+/g;

function trimUrl(url: string): string {
  return url.replace(/[.,;:!?]+$/, "");
}

// linkKind: preview — зарегистрированный dev-сервер сессии, pr — pull request,
// остальное — внешняя ссылка.
function linkKind(url: string, previewUrls: ReadonlySet<string>): LinkKind {
  if (previewUrls.has(url)) return "preview";
  if (/\/(pull|merge_requests)\/\d+/.test(url)) return "pr";
  return "external";
}

// prettyUrl — компактная подпись для голой ссылки: хост плюс путь без хвостового слеша.
function prettyUrl(url: string): string {
  try {
    const u = new URL(url);
    const path = u.pathname.replace(/\/$/, "");
    return `${u.host}${path}`;
  } catch {
    return url;
  }
}

export function extractLinks(
  messages: readonly ThreadMessage[],
  previewUrls: ReadonlySet<string>,
): SessionLink[] {
  const out: SessionLink[] = [];
  const seen = new Set<string>();

  const push = (
    url: string,
    label: string,
    sourceId: string,
    sourceIndex: number,
  ) => {
    const key = `${sourceId}|${url}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push({
      kind: linkKind(url, previewUrls),
      label,
      url,
      sourceId,
      sourceIndex,
    });
  };

  messages.forEach((message, index) => {
    if (message.role !== "assistant") return;
    const text = messagePlainText(message);
    if (!text) return;

    // Сначала markdown-ссылки, затем голые URL в остатке: иначе адрес из markdown
    // попал бы в список дважды — с подписью и без.
    const rest = text.replace(MD_LINK, (_, label: string, url: string) => {
      push(trimUrl(url), label.trim(), message.id, index);
      return " ";
    });
    for (const match of rest.matchAll(BARE_URL)) {
      const url = trimUrl(match[0]);
      push(url, prettyUrl(url), message.id, index);
    }
  });

  return out;
}
