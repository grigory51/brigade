package name.ozhegov.brigade.shared.auth

import name.ozhegov.brigade.shared.model.LoginResponse
import name.ozhegov.brigade.shared.net.AuthService

// SessionManager управляет токенами авторизации поверх TokenStore: выполняет логин,
// сохраняет токены, отдаёт access-токен для исходящих запросов и обновляет его по refresh.
class SessionManager(
    private val authService: AuthService,
    private val tokenStore: TokenStore,
) {
    // accessToken используется ConnectClient.TokenProvider для заголовка Authorization.
    fun accessToken(): String? = tokenStore.accessToken()

    fun isLoggedIn(): Boolean = tokenStore.accessToken() != null

    suspend fun login(username: String, password: String): LoginResponse {
        val resp = authService.login(username, password)
        tokenStore.save(resp.accessToken, resp.refreshToken)
        return resp
    }

    // refresh обновляет access-токен по сохранённому refresh-токену. Возвращает true при успехе.
    suspend fun refresh(): Boolean {
        val refresh = tokenStore.refreshToken() ?: return false
        val resp = authService.refresh(refresh)
        tokenStore.save(resp.accessToken, resp.refreshToken)
        return true
    }

    suspend fun logout() {
        runCatching { authService.logout() }
        tokenStore.clear()
    }
}
