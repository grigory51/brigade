package name.ozhegov.brigade.shared.acp

import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.preparePost
import io.ktor.client.request.setBody
import io.ktor.client.statement.bodyAsChannel
import io.ktor.http.ContentType
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import io.ktor.utils.io.ByteReadChannel
import io.ktor.utils.io.readUTF8Line
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.serialization.encodeToString
import name.ozhegov.brigade.shared.net.ConnectClient
import name.ozhegov.brigade.shared.net.ConnectException
import name.ozhegov.brigade.shared.net.TokenProvider
import name.ozhegov.brigade.shared.net.brigadeJson

// AcpClient — клиент ACP-режима поверх канонического AG-UI/SSE. Заменяет прежнюю
// самодельную WS-обвязку ACP (WS-эндпоинт удалён на бэкенде). Контракт:
//   POST /api/ag-ui/run        — RunAgentInput → text/event-stream (поток AgUiEvent);
//   POST /api/ag-ui/permission — ответ на запрос разрешения (human-in-the-loop).
// Авторизация — Authorization: Bearer <access-jwt> на каждый запрос (тикеты остались
// только у CLI-терминала). threadId запроса = brigade sessionID.
//
// SSE-поток разбирается вручную (построчно `data: {json}`), чтобы не вводить зависимость
// ktor-client-sse: её API в ktor 2.3.x ограничен, а формат кадров тривиален.
class AcpClient(
    private val connect: ConnectClient,
    private val tokenProvider: TokenProvider,
) {
    // run открывает прогон turn'а: отправляет RunAgentInput и возвращает холодный Flow
    // канонических событий AG-UI. Flow завершается, когда сервер закрывает SSE-поток
    // (после RUN_FINISHED либо RUN_ERROR). Незнакомые типы событий маппятся в UNKNOWN и
    // не прерывают разбор.
    fun run(input: RunAgentInput): Flow<AgUiEvent> = flow {
        val statement = connect.httpClient.preparePost("${connect.base}/api/ag-ui/run") {
            contentType(ContentType.Application.Json)
            bearer()?.let { header("Authorization", "Bearer $it") }
            setBody(brigadeJson.encodeToString(input))
        }
        statement.execute { response ->
            if (!response.status.isSuccess()) {
                throw ConnectException(
                    status = response.status.value,
                    code = null,
                    message = "AG-UI run failed: HTTP ${response.status.value}",
                )
            }
            val channel = response.bodyAsChannel()
            readSseEvents(channel) { json -> emit(brigadeJson.decodeFromString<AgUiEvent>(json)) }
        }
    }

    // respondPermission отправляет решение пользователя по запросу разрешения отдельным
    // POST (SSE однонаправлен). id берётся из PermissionRequest, decision — выбранный
    // optionId. threadId — идентификатор сессии (он же threadId прогона).
    suspend fun respondPermission(threadId: String, id: String, decision: String) {
        val response = connect.httpClient.post("${connect.base}/api/ag-ui/permission") {
            contentType(ContentType.Application.Json)
            bearer()?.let { header("Authorization", "Bearer $it") }
            setBody(brigadeJson.encodeToString(PermissionResponse(threadId, id, decision)))
        }
        if (!response.status.isSuccess()) {
            throw ConnectException(
                status = response.status.value,
                code = null,
                message = "AG-UI permission failed: HTTP ${response.status.value}",
            )
        }
    }

    private suspend fun bearer(): String? = tokenProvider.accessToken()
}

// readSseEvents читает text/event-stream построчно и для каждого завершённого кадра
// (пустая строка-разделитель) вызывает onEvent с накопленным значением data. Формат
// бэкенда — единственное поле data на кадр: `data: {json}\n\n`. Поддержан и многострочный
// data (склейка через \n) на случай форматирования по спецификации SSE.
private suspend inline fun readSseEvents(
    channel: ByteReadChannel,
    onEvent: (String) -> Unit,
) {
    val data = StringBuilder()
    while (true) {
        val line = channel.readUTF8Line() ?: break
        when {
            // Пустая строка завершает кадр: эмитим накопленный data, если он не пуст.
            line.isEmpty() -> {
                if (data.isNotEmpty()) {
                    onEvent(data.toString())
                    data.clear()
                }
            }
            // Строки-комментарии SSE (начинаются с ':') игнорируются.
            line.startsWith(":") -> {}
            line.startsWith("data:") -> {
                if (data.isNotEmpty()) data.append('\n')
                data.append(line.removePrefix("data:").trimStart())
            }
            // Прочие поля кадра (event:/id:/retry:) бэкендом не используются — пропускаем.
            else -> {}
        }
    }
    // Хвост без завершающей пустой строки (поток закрыт сразу после data).
    if (data.isNotEmpty()) onEvent(data.toString())
}
