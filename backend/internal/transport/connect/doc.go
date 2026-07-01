// Package connectsvc реализует ConnectRPC-сервисы из proto-контракта (brigade.v1):
// AuthService, SessionService и AgentService.
//
// Слой тонкий: хендлеры извлекают аутентифицированного пользователя из контекста
// (его кладёт auth.Interceptor), делегируют доменам (auth.Service, session.Registry,
// auth.TicketStore) и переводят доменные сущности и ошибки в proto-типы и connect-коды.
// Здесь же — единственное место маппинга между proto-перечислениями
// (SessionMode/Kind/Status) и строковыми значениями store.
package connectsvc

import "github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"

// Реализации удовлетворяют сгенерированным интерфейсам хендлеров — проверка на этапе
// компиляции, чтобы расхождение с proto-контрактом ловилось сразу.
var (
	_ brigadev1connect.AuthServiceHandler    = (*AuthService)(nil)
	_ brigadev1connect.SessionServiceHandler = (*SessionService)(nil)
	_ brigadev1connect.AgentServiceHandler   = (*AgentService)(nil)
)
