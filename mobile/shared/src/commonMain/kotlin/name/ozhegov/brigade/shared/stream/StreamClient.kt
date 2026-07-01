package name.ozhegov.brigade.shared.stream

import io.ktor.client.plugins.websocket.DefaultClientWebSocketSession
import io.ktor.client.plugins.websocket.webSocketSession
import name.ozhegov.brigade.shared.net.ConnectClient

// StreamClient — WS-обвязка терминала CLI-сессии (pty + xterm). Использует уже
// сконфигурированный HttpClient из ConnectClient (плагин WebSockets ставится в
// buildHttpClient), чтобы не плодить второй клиент и переиспользовать движок платформы.
//
// Относится только к CLI-режиму: ACP переведён на канонический AG-UI поверх SSE
// (см. AcpClient). Доступ к терминалу авторизуется одноразовым тикетом: UI вызывает
// SessionService.issueStreamTicket(sessionId), затем открывает WS с этим тикетом
// (браузер/клиент не может выставить заголовок Authorization при апгрейде соединения).
class StreamClient(
    private val connect: ConnectClient,
) {
    // openTerminal открывает WS терминала CLI-сессии. Эндпоинт бэкенда —
    // GET /ws/terminal/{sessionId}?ticket=<ticket>. Возвращает «сырую» сессию ktor;
    // чтение/запись кадров псевдотерминала — на стороне вызывающего кода.
    suspend fun openTerminal(sessionId: String, ticket: String): DefaultClientWebSocketSession {
        // base — http(s)://host:port; для WS подменяем схему на ws/wss. URL собираем
        // строкой, чтобы не зависеть от различий URLBuilder между версиями ktor.
        val wsBase = connect.base
            .replaceFirst("https://", "wss://")
            .replaceFirst("http://", "ws://")
            .trimEnd('/')
        return connect.httpClient.webSocketSession("$wsBase/ws/terminal/$sessionId?ticket=$ticket")
    }
}
