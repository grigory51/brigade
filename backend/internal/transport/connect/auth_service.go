package connectsvc

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/auth"
)

// AuthService реализует brigade.v1.AuthService поверх auth.Service.
type AuthService struct {
	svc *auth.Service
}

// NewAuthService собирает реализацию AuthService.
func NewAuthService(svc *auth.Service) *AuthService {
	return &AuthService{svc: svc}
}

// Login проверяет учётные данные, выпускает пару токенов и для web-клиента выставляет
// access-токен httpOnly-cookie (mobile использует access_token из тела как Bearer).
func (s *AuthService) Login(ctx context.Context, req *connect.Request[v1.LoginRequest]) (*connect.Response[v1.LoginResponse], error) {
	pair, err := s.svc.Login(ctx, req.Msg.Username, req.Msg.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&v1.LoginResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		User:         userToProto(pair.User),
	})
	setAccessCookie(resp.Header(), pair.AccessToken, pair.AccessExpiresAt)
	setRefreshCookie(resp.Header(), pair.RefreshToken, pair.RefreshExpiresAt)
	return resp, nil
}

// Refresh обменивает refresh-токен на новую пару и обновляет cookie web-клиента.
// Источник refresh-токена: тело запроса (mobile) либо httpOnly-cookie brigade_refresh
// (web — токен не хранится в JS и переживает перезагрузку). Ротация инвалидирует
// прежний refresh-токен, поэтому новый кладётся и в тело, и в обновлённую cookie.
func (s *AuthService) Refresh(ctx context.Context, req *connect.Request[v1.RefreshRequest]) (*connect.Response[v1.RefreshResponse], error) {
	refreshToken := req.Msg.RefreshToken
	if refreshToken == "" {
		refreshToken = auth.RefreshTokenFromHeader(req.Header())
	}

	pair, err := s.svc.Refresh(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&v1.RefreshResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
	})
	setAccessCookie(resp.Header(), pair.AccessToken, pair.AccessExpiresAt)
	setRefreshCookie(resp.Header(), pair.RefreshToken, pair.RefreshExpiresAt)
	return resp, nil
}

// Me возвращает текущего пользователя по проверенному access-токену из контекста.
func (s *AuthService) Me(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.User], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	return connect.NewResponse(&v1.User{Id: u.ID, Username: u.Username}), nil
}

// Logout отзывает refresh-токен пользователя и очищает access-cookie web-клиента.
// Требует аутентификации (refresh-токены отзываются для текущего пользователя).
func (s *AuthService) Logout(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.Empty], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.LogoutAll(ctx, u.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&v1.Empty{})
	clearAccessCookie(resp.Header())
	clearRefreshCookie(resp.Header())
	return resp, nil
}

// GetClaudeSettings возвращает состояние Claude-настроек текущего пользователя
// (только флаг token_set — значение токена не раскрывается).
func (s *AuthService) GetClaudeSettings(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ClaudeSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	set, err := s.svc.ClaudeTokenSet(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ClaudeSettings{TokenSet: set}), nil
}

// SetClaudeToken задаёт (или очищает) подписочный токен Claude текущего пользователя.
func (s *AuthService) SetClaudeToken(ctx context.Context, req *connect.Request[v1.SetClaudeTokenRequest]) (*connect.Response[v1.ClaudeSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.SetClaudeToken(ctx, u.ID, strings.TrimSpace(req.Msg.Token)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ClaudeSettings{TokenSet: strings.TrimSpace(req.Msg.Token) != ""}), nil
}

// userToProto переводит доменного пользователя auth в proto-сообщение.
func userToProto(u auth.User) *v1.User {
	return &v1.User{Id: u.ID, Username: u.Username}
}

// setAccessCookie добавляет в ответ Set-Cookie с access-токеном (httpOnly) для web.
// SameSite=Lax и Path=/ покрывают и unary-вызовы, и WS-апгрейд того же origin.
func setAccessCookie(h http.Header, token string, expiresAt time.Time) {
	c := &http.Cookie{
		Name:     auth.AccessCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// clearAccessCookie добавляет Set-Cookie, удаляющий access-cookie (logout).
func clearAccessCookie(h http.Header) {
	c := &http.Cookie{
		Name:     auth.AccessCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// setRefreshCookie добавляет в ответ Set-Cookie с refresh-токеном (httpOnly) для web.
// Долгоживущая cookie (TTL = refresh_ttl) переживает перезагрузку страницы и закрытие
// вкладки, давая фронту тихо обновлять короткий access-токен через Refresh.
func setRefreshCookie(h http.Header, token string, expiresAt time.Time) {
	c := &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// clearRefreshCookie добавляет Set-Cookie, удаляющий refresh-cookie (logout).
func clearRefreshCookie(h http.Header) {
	c := &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}
