package name.ozhegov.brigade.shared.net

import io.ktor.client.HttpClient
import io.ktor.client.engine.HttpClientEngine
import io.ktor.serialization.kotlinx.json.json

// Платформенный движок Ktor: OkHttp на Android, Darwin на iOS. connect-kotlin на iOS-таргете
// отсутствует, поэтому общий транспорт — Ktor поверх Connect-JSON.
expect fun httpClientEngine(): HttpClientEngine

// Общий конструктор HttpClient. WebSockets подключаются здесь же, так как термин/ACP-стримы
// идут по тому же клиенту.
internal fun buildHttpClient(): HttpClient = HttpClient(httpClientEngine()) {
    install(io.ktor.client.plugins.websocket.WebSockets)
    install(io.ktor.client.plugins.contentnegotiation.ContentNegotiation) {
        json(name.ozhegov.brigade.shared.net.brigadeJson)
    }
}
