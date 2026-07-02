import { sessionClient } from "./client";

/**
 * Запрашивает короткоживущий тикет и строит WebSocket-URL для стрима сессии.
 *
 * Авторизация WS не может опираться на httpOnly-cookie напрямую: при апгрейде
 * браузер не даёт выставить заголовки, а cookie на cross-origin dev-прокси не
 * всегда доходит. Поэтому используется одноразовый ticket из unary-вызова,
 * который передаётся в query-параметре.
 *
 * kind определяет неймспейс пути: terminal (терминал агента CLI-сессии) или
 * shell (вспомогательный шелл рядом с любой сессией).
 */
export async function streamUrl(
  kind: "terminal" | "shell",
  sessionId: string,
): Promise<string> {
  const { ticket } = await sessionClient.issueStreamTicket({ sessionId });
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const base = `${proto}//${window.location.host}`;
  const params = new URLSearchParams({ ticket });
  return `${base}/ws/${kind}/${encodeURIComponent(sessionId)}?${params.toString()}`;
}
