package name.ozhegov.brigade.shared.acp

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement

// Модели канонического AG-UI protocol (@ag-ui/core) под Connect/JSON. Источник истины —
// backend/internal/agui/event.go (Go-структуры тех же событий). Mobile ест тот же поток
// SSE, что и web (@ag-ui/client), поэтому имена полей в JSON строго каноничны (camelCase).

// --- запрос: RunAgentInput ---

// RunAgentInput — тело POST /api/ag-ui/run. threadId = brigade sessionID. Бэкенд
// использует подмножество канона: threadId, runId, messages, tools (state/context не
// интерпретируются в режиме ACP brigade).
@Serializable
data class RunAgentInput(
    val threadId: String,
    val runId: String,
    val messages: List<InputMessage> = emptyList(),
    val tools: List<InputTool> = emptyList(),
)

// InputMessage — сообщение истории. Для prompt бэкенд берёт текст последнего сообщения
// с role="user".
@Serializable
data class InputMessage(
    val id: String,
    val role: String,
    val content: String,
)

// InputTool — описание frontend-tool: parameters — JSON Schema входных параметров.
@Serializable
data class InputTool(
    val name: String,
    val description: String = "",
    val parameters: JsonElement? = null,
)

// --- ответ: поток событий AG-UI ---

// AgUiEventType — дискриминатор события (поле type). Значения соответствуют
// @ag-ui/core EventType и event.go. UNKNOWN — forward-совместимость: незнакомый тип не
// должен ронять разбор потока.
@Serializable
enum class AgUiEventType {
    @SerialName("RUN_STARTED")
    RUN_STARTED,

    @SerialName("RUN_FINISHED")
    RUN_FINISHED,

    @SerialName("RUN_ERROR")
    RUN_ERROR,

    @SerialName("TEXT_MESSAGE_START")
    TEXT_MESSAGE_START,

    @SerialName("TEXT_MESSAGE_CONTENT")
    TEXT_MESSAGE_CONTENT,

    @SerialName("TEXT_MESSAGE_END")
    TEXT_MESSAGE_END,

    @SerialName("REASONING_START")
    REASONING_START,

    @SerialName("REASONING_MESSAGE_START")
    REASONING_MESSAGE_START,

    @SerialName("REASONING_MESSAGE_CONTENT")
    REASONING_MESSAGE_CONTENT,

    @SerialName("REASONING_MESSAGE_END")
    REASONING_MESSAGE_END,

    @SerialName("REASONING_END")
    REASONING_END,

    @SerialName("TOOL_CALL_START")
    TOOL_CALL_START,

    @SerialName("TOOL_CALL_ARGS")
    TOOL_CALL_ARGS,

    @SerialName("TOOL_CALL_END")
    TOOL_CALL_END,

    @SerialName("TOOL_CALL_RESULT")
    TOOL_CALL_RESULT,

    @SerialName("STATE_SNAPSHOT")
    STATE_SNAPSHOT,

    @SerialName("CUSTOM")
    CUSTOM,

    UNKNOWN,
}

// AgUiEvent — обобщённое событие AG-UI: одна структура на все варианты, незаполненные
// поля опускаются (encodeDefaults=false на сериализаторе). Имена полей — канонические.
//
// permission_request (human-in-the-loop) приходит как CUSTOM с name="permission_request"
// и value=PermissionRequest; usage_update — как CUSTOM name="usage", value=Usage. Value
// оставлен сырым JsonElement, чтобы разобрать его по name на стороне UI.
@Serializable
data class AgUiEvent(
    val type: AgUiEventType = AgUiEventType.UNKNOWN,

    val threadId: String? = null,
    val runId: String? = null,

    val messageId: String? = null,
    val delta: String? = null,
    val role: String? = null,

    val toolCallId: String? = null,
    val toolCallName: String? = null,
    val content: String? = null,

    val snapshot: JsonElement? = null,
    val result: JsonElement? = null,

    // name/value — полезная нагрузка CUSTOM.
    val name: String? = null,
    val value: JsonElement? = null,

    // message/code — для RUN_ERROR.
    val message: String? = null,
    val code: String? = null,
)

// --- permission (human-in-the-loop) ---

// PermissionRequest — запрос разрешения, приходит как value события CUSTOM
// (name="permission_request"). Клиент возвращает выбранный optionId через respondPermission.
@Serializable
data class PermissionRequest(
    val id: String = "",
    val title: String = "",
    val toolCallId: String? = null,
    val options: List<PermissionOption> = emptyList(),
)

// PermissionOption — вариант ответа. kind: allow_once/allow_always/reject_once/reject_always.
@Serializable
data class PermissionOption(
    val optionId: String = "",
    val name: String = "",
    val kind: String = "",
)

// PermissionResponse — тело POST /api/ag-ui/permission. id — из PermissionRequest,
// decision — выбранный optionId.
@Serializable
data class PermissionResponse(
    val threadId: String,
    val id: String,
    val decision: String,
)

// --- usage (CUSTOM name="usage") ---

// Usage — расход контекста и стоимость сессии, приходит как value события CUSTOM
// (name="usage").
@Serializable
data class Usage(
    val used: Int = 0,
    val size: Int = 0,
    val cost: Cost? = null,
)

@Serializable
data class Cost(
    val amount: Double = 0.0,
    val currency: String = "",
)
