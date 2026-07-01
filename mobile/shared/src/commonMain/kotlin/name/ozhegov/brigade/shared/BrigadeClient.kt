package name.ozhegov.brigade.shared

import name.ozhegov.brigade.shared.acp.AcpClient
import name.ozhegov.brigade.shared.auth.SessionManager
import name.ozhegov.brigade.shared.auth.TokenStore
import name.ozhegov.brigade.shared.net.AgentService
import name.ozhegov.brigade.shared.net.AuthService
import name.ozhegov.brigade.shared.net.ConnectClient
import name.ozhegov.brigade.shared.net.SessionService
import name.ozhegov.brigade.shared.net.TokenProvider
import name.ozhegov.brigade.shared.stream.StreamClient

// BrigadeClient — единая точка доступа к бэкенду из UI. Собирает ConnectClient с
// авторизацией через TokenStore и публикует доменные сервисы, менеджер сессии и клиенты
// двух режимов взаимодействия: CLI-терминал (StreamClient, WS) и ACP (AcpClient,
// AG-UI/SSE). baseUrl задаётся платформой (для Android-эмулятора — http://10.0.2.2:8080,
// для iOS-симулятора — http://localhost:8080).
class BrigadeClient(
    baseUrl: String,
    tokenStore: TokenStore,
) {
    private val tokenProvider = TokenProvider { tokenStore.accessToken() }

    private val connect = ConnectClient(
        baseUrl = baseUrl,
        tokenProvider = tokenProvider,
    )

    val auth: AuthService = AuthService(connect)
    val sessions: SessionService = SessionService(connect)
    val agents: AgentService = AgentService(connect)

    val session: SessionManager = SessionManager(auth, tokenStore)

    // stream — WS терминала CLI-сессии; тикет выдаёт sessions.issueStreamTicket.
    val stream: StreamClient = StreamClient(connect)

    // acp — ACP-режим поверх AG-UI/SSE (POST /api/ag-ui/run, авторизация Bearer).
    val acp: AcpClient = AcpClient(connect, tokenProvider)
}
