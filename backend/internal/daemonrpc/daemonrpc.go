// Package daemonrpc — общая обвязка клиентов AgentDaemonService (демон среды агента).
//
// К демону ходят двое: acpremote (ACP-сессия) и cliremote (durable-терминал CLI-агента).
// Транспорт и авторизация у них одни и те же — асимметричная подпись brigade, токен в
// заголовке на каждый вызов, — и держать это в двух местах значит править схему доступа
// дважды. Различаются клиенты набором вызываемых методов, а не способом достучаться.
package daemonrpc

import (
	"log"
	"net/http"

	"connectrpc.com/connect"

	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

// Conn — подключение к демону: сгенерированный клиент плюс источник подписи. Встраивается
// в клиентов, поэтому RPC у них вызывается как c.RPC.Method(...).
type Conn struct {
	RPC       brigadev1connect.AgentDaemonServiceClient
	signToken func() (string, error)
	name      string // имя клиента для логов (acpremote / cliremote)
}

// Dial собирает подключение к демону по baseURL (http://<host>:<port>). signToken выдаёт
// свежий подписанный токен на каждый вызов; name попадает в сообщения об ошибках подписи.
func Dial(baseURL, name string, signToken func() (string, error)) Conn {
	return Conn{
		RPC:       brigadev1connect.NewAgentDaemonServiceClient(http.DefaultClient, baseURL),
		signToken: signToken,
		name:      name,
	}
}

// Sign выписывает свежий подписанный токен; ошибку логирует и возвращает пустую строку
// (демон отвергнет такой вызов как Unauthenticated).
func (c Conn) Sign() string {
	t, err := c.signToken()
	if err != nil {
		log.Printf("%s: sign daemon token: %v", c.name, err)
		return ""
	}
	return t
}

// Req оборачивает сообщение в connect.Request с подписанным токеном в Authorization.
// Свободная функция, а не метод: у методов в Go не может быть своих type-параметров.
func Req[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	return r
}
