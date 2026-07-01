package name.ozhegov.brigade.shared.model

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

// Модели контракта brigade.v1 под Connect-JSON. Здесь они описаны вручную как
// @Serializable data class. Источник истины — proto/brigade/v1/*.proto; при появлении
// kotlin-кодогенерации (buf) эти классы заменяются сгенерированными.
//
// Connect-JSON для proto3 сериализует поля в lowerCamelCase, enum — как строки с полным
// именем значения (например "SESSION_MODE_LOCAL"), int64 — как строку. Это учтено в
// аннотациях @SerialName и кастомных enum-сериализаторах ниже.

// --- auth.proto ---

@Serializable
data class User(
    val id: String = "",
    val username: String = "",
)

@Serializable
data class LoginRequest(
    val username: String,
    val password: String,
)

@Serializable
data class LoginResponse(
    val accessToken: String = "",
    val refreshToken: String = "",
    val user: User? = null,
)

@Serializable
data class RefreshRequest(
    val refreshToken: String,
)

@Serializable
data class RefreshResponse(
    val accessToken: String = "",
    val refreshToken: String = "",
)

// Пустое тело для методов без полезной нагрузки (Me/Logout).
@Serializable
class Empty

// --- session.proto ---

@Serializable
enum class SessionMode {
    @SerialName("SESSION_MODE_UNSPECIFIED")
    UNSPECIFIED,

    @SerialName("SESSION_MODE_LOCAL")
    LOCAL,

    @SerialName("SESSION_MODE_DOCKER")
    DOCKER,
}

@Serializable
enum class SessionKind {
    @SerialName("SESSION_KIND_UNSPECIFIED")
    UNSPECIFIED,

    @SerialName("SESSION_KIND_CLI")
    CLI,

    @SerialName("SESSION_KIND_ACP")
    ACP,
}

@Serializable
enum class SessionStatus {
    @SerialName("SESSION_STATUS_UNSPECIFIED")
    UNSPECIFIED,

    @SerialName("SESSION_STATUS_RUNNING")
    RUNNING,

    @SerialName("SESSION_STATUS_STOPPED")
    STOPPED,

    @SerialName("SESSION_STATUS_FAILED")
    FAILED,
}

@Serializable
data class Session(
    val id: String = "",
    val userId: String = "",
    val mode: SessionMode = SessionMode.UNSPECIFIED,
    val kind: SessionKind = SessionKind.UNSPECIFIED,
    val agentType: String = "",
    val agentSessionId: String = "",
    val containerLabel: String = "",
    val status: SessionStatus = SessionStatus.UNSPECIFIED,
    val cwd: String = "",
    // created_at — Unix-время в секундах; Connect-JSON кодирует int64 строкой.
    val createdAt: String = "0",
)

@Serializable
data class CreateSessionRequest(
    val agentType: String,
    val mode: SessionMode,
    val kind: SessionKind,
    val prompt: String = "",
    val cwd: String = "",
)

@Serializable
data class CreateSessionResponse(
    val session: Session? = null,
)

@Serializable
class ListSessionsRequest

@Serializable
data class ListSessionsResponse(
    val sessions: List<Session> = emptyList(),
)

@Serializable
data class GetSessionRequest(
    val sessionId: String,
)

@Serializable
data class GetSessionResponse(
    val session: Session? = null,
)

@Serializable
data class StopSessionRequest(
    val sessionId: String,
)

@Serializable
data class DeleteSessionRequest(
    val sessionId: String,
)

@Serializable
data class IssueStreamTicketRequest(
    val sessionId: String,
)

@Serializable
data class IssueStreamTicketResponse(
    val ticket: String = "",
    // expires_at — Unix-время в секундах; int64 в Connect-JSON приходит строкой.
    val expiresAt: String = "0",
)

// --- agent.proto ---

// Режим взаимодействия (CLI/ACP) задаётся через SessionKind при создании сессии и
// не зависит от агента, поэтому AgentType описывает только идентификатор и имя
// (см. proto/brigade/v1/agent.proto).
@Serializable
data class AgentType(
    val id: String = "",
    val name: String = "",
)

@Serializable
class ListAgentTypesRequest

@Serializable
data class ListAgentTypesResponse(
    val agentTypes: List<AgentType> = emptyList(),
)
