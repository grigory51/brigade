package name.ozhegov.brigade.shared.net

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.http.ContentType
import io.ktor.http.HttpStatusCode
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json

// Общая конфигурация JSON под Connect-JSON: неизвестные поля игнорируются (forward-совместимость
// при расширении контракта), значения по умолчанию не сериализуются — proto3 трактует их как нули.
val brigadeJson: Json = Json {
    ignoreUnknownKeys = true
    explicitNulls = false
    encodeDefaults = false
}

// Ошибка вызова Connect-эндпоинта. status — HTTP-код, code — Connect-код ошибки из тела
// (например "unauthenticated"), message — человекочитаемое сообщение.
class ConnectException(
    val status: Int,
    val code: String?,
    override val message: String,
) : Exception(message)

// Поставщик access-токена для авторизованных вызовов. Реализуется хранилищем токенов;
// возврат null означает отсутствие сессии (запрос уйдёт без заголовка Authorization).
fun interface TokenProvider {
    suspend fun accessToken(): String?
}

// ConnectClient — тонкая обёртка над Ktor для unary-вызовов ConnectRPC в JSON-режиме.
// Каждый метод proto-сервиса — это POST /<package>.<Service>/<Method> с JSON-телом запроса
// и JSON-телом ответа. baseUrl — корень бэкенда без завершающего слэша, например
// "http://10.0.2.2:8080" (10.0.2.2 — хост-машина из Android-эмулятора).
class ConnectClient(
    private val baseUrl: String,
    private val http: HttpClient = buildHttpClient(),
    private val tokenProvider: TokenProvider = TokenProvider { null },
) {
    val httpClient: HttpClient get() = http
    val base: String get() = baseUrl

    // call выполняет unary-вызов: сериализует req, отправляет POST, разбирает ответ или
    // бросает ConnectException на не-2xx. Path — это "brigade.v1.AuthService/Login" и т.п.
    suspend inline fun <reified Req, reified Res> call(path: String, req: Req): Res {
        val response = rawPost(path, brigadeJson.encodeToString(req))
        val text = response.body<String>()
        if (!response.status.isSuccess()) {
            throw parseError(response.status, text)
        }
        return brigadeJson.decodeFromString(text)
    }

    @PublishedApi
    internal suspend fun rawPost(path: String, jsonBody: String): HttpResponse {
        val token = tokenProvider.accessToken()
        return http.post("$baseUrl/$path") {
            contentType(ContentType.Application.Json)
            if (token != null) {
                header("Authorization", "Bearer $token")
            }
            setBody(jsonBody)
        }
    }

    @PublishedApi
    internal fun parseError(status: HttpStatusCode, body: String): ConnectException {
        // Тело ошибки Connect-unary: {"code":"...","message":"..."}.
        val code = runCatching {
            brigadeJson.decodeFromString<ConnectErrorBody>(body)
        }.getOrNull()
        return ConnectException(
            status = status.value,
            code = code?.code,
            message = code?.message ?: "HTTP ${status.value}: ${status.description}",
        )
    }
}

@kotlinx.serialization.Serializable
internal data class ConnectErrorBody(
    val code: String? = null,
    val message: String? = null,
)
