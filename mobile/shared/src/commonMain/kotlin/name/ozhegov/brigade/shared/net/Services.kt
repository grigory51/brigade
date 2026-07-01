package name.ozhegov.brigade.shared.net

import name.ozhegov.brigade.shared.model.CreateSessionRequest
import name.ozhegov.brigade.shared.model.CreateSessionResponse
import name.ozhegov.brigade.shared.model.DeleteSessionRequest
import name.ozhegov.brigade.shared.model.Empty
import name.ozhegov.brigade.shared.model.GetSessionRequest
import name.ozhegov.brigade.shared.model.GetSessionResponse
import name.ozhegov.brigade.shared.model.IssueStreamTicketRequest
import name.ozhegov.brigade.shared.model.IssueStreamTicketResponse
import name.ozhegov.brigade.shared.model.ListAgentTypesRequest
import name.ozhegov.brigade.shared.model.ListAgentTypesResponse
import name.ozhegov.brigade.shared.model.ListSessionsRequest
import name.ozhegov.brigade.shared.model.ListSessionsResponse
import name.ozhegov.brigade.shared.model.LoginRequest
import name.ozhegov.brigade.shared.model.LoginResponse
import name.ozhegov.brigade.shared.model.RefreshRequest
import name.ozhegov.brigade.shared.model.RefreshResponse
import name.ozhegov.brigade.shared.model.StopSessionRequest
import name.ozhegov.brigade.shared.model.User

// Доменные обёртки над ConnectClient. Пути соответствуют service/rpc из proto/brigade/v1.

class AuthService(private val client: ConnectClient) {
    suspend fun login(username: String, password: String): LoginResponse =
        client.call("brigade.v1.AuthService/Login", LoginRequest(username, password))

    suspend fun refresh(refreshToken: String): RefreshResponse =
        client.call("brigade.v1.AuthService/Refresh", RefreshRequest(refreshToken))

    suspend fun me(): User =
        client.call("brigade.v1.AuthService/Me", Empty())

    suspend fun logout(): Empty =
        client.call("brigade.v1.AuthService/Logout", Empty())
}

class SessionService(private val client: ConnectClient) {
    suspend fun create(req: CreateSessionRequest): CreateSessionResponse =
        client.call("brigade.v1.SessionService/Create", req)

    suspend fun list(): ListSessionsResponse =
        client.call("brigade.v1.SessionService/List", ListSessionsRequest())

    suspend fun get(sessionId: String): GetSessionResponse =
        client.call("brigade.v1.SessionService/Get", GetSessionRequest(sessionId))

    suspend fun stop(sessionId: String): Empty =
        client.call("brigade.v1.SessionService/Stop", StopSessionRequest(sessionId))

    suspend fun delete(sessionId: String): Empty =
        client.call("brigade.v1.SessionService/Delete", DeleteSessionRequest(sessionId))

    suspend fun issueStreamTicket(sessionId: String): IssueStreamTicketResponse =
        client.call("brigade.v1.SessionService/IssueStreamTicket", IssueStreamTicketRequest(sessionId))
}

class AgentService(private val client: ConnectClient) {
    suspend fun listAgentTypes(): ListAgentTypesResponse =
        client.call("brigade.v1.AgentService/ListAgentTypes", ListAgentTypesRequest())
}
