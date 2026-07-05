package auth

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// AccessCookieName — имя httpOnly-cookie с access-токеном для web-клиента.
const AccessCookieName = "brigade_access"

// RefreshCookieName — имя httpOnly-cookie с refresh-токеном для web-клиента. Хранится
// в cookie (а не в JS), чтобы переживать перезагрузку страницы и быть недоступным для
// XSS; эндпоинт Refresh читает его, когда тело запроса не несёт refresh-токен (web).
const RefreshCookieName = "brigade_refresh"

// RefreshTokenFromHeader извлекает refresh-токен из cookie запроса (web-клиент). Пустая
// строка — cookie отсутствует. Используется хендлером Refresh, когда тело пустое.
func RefreshTokenFromHeader(header http.Header) string {
	if c, err := cookieFromHeader(header)(RefreshCookieName); err == nil {
		return c.Value
	}
	return ""
}

// contextKey — приватный тип ключа контекста, исключает коллизии с чужими ключами.
type contextKey struct{}

var userKey contextKey

// withUser кладёт пользователя в контекст.
func withUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFromContext извлекает пользователя, положенного middleware/интерсептором.
// ok=false означает неаутентифицированный запрос.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok
}

// tokenFromHeaderOrCookie извлекает access-токен из запроса: сперва Bearer-заголовок
// (mobile), затем httpOnly-cookie (web). Пустая строка — токен не предоставлен.
func tokenFromHeaderOrCookie(h http.Header, cookie func(string) (*http.Cookie, error)) string {
	if auth := h.Get("Authorization"); auth != "" {
		if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	if c, err := cookie(AccessCookieName); err == nil {
		return c.Value
	}
	return ""
}

// Interceptor — Connect-интерсептор для unary- и stream-вызовов. Кладёт User в
// контекст при валидном токене (Bearer или cookie); обязательность
// авторизации проверяет сам хендлер сервиса. Cookie читается из заголовка Cookie
// запроса, так как connect.Request не даёт прямого доступа к *http.Request.
func (s *Service) Interceptor() connect.Interceptor {
	return interceptor{svc: s}
}

type interceptor struct {
	svc *Service
}

func (i interceptor) authedContext(ctx context.Context, header http.Header) context.Context {
	token := tokenFromHeaderOrCookie(header, cookieFromHeader(header))
	if token == "" {
		return ctx
	}
	claims, err := i.svc.jwt.Verify(token)
	if err != nil {
		return ctx
	}
	return withUser(ctx, User{ID: claims.Subject, Username: claims.Username})
}

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		return next(i.authedContext(ctx, req.Header()), req)
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.authedContext(ctx, conn.RequestHeader()), conn)
	}
}

// cookieFromHeader строит функцию чтения cookie по сырому заголовку Cookie,
// используя стандартный парсер net/http (через временный Request).
func cookieFromHeader(header http.Header) func(string) (*http.Cookie, error) {
	r := &http.Request{Header: http.Header{"Cookie": header.Values("Cookie")}}
	return r.Cookie
}
