package connectsvc

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/agentimage"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/codexlogin"
	"github.com/grigory51/brigade/backend/internal/runtimecfg"
)

// AuthService реализует brigade.v1.AuthService поверх auth.Service.
type AuthService struct {
	svc        *auth.Service
	images     *agentimage.Service
	runtime    *runtimecfg.Service
	desktop    bool
	version    string
	password   bool
	oidcName   string
	oidc       *auth.OIDC
	secure     bool
	codexLogin *codexlogin.Service
}

func (s *AuthService) SetCodexLogin(service *codexlogin.Service) { s.codexLogin = service }

// NewAuthService собирает реализацию AuthService. notify может быть nil — тогда проверка
// уведомлений недоступна, остальные методы работают. images — образы контейнера агента
// (в local-режиме сервис отвечает «недоступно»); runtime — режим исполнения сессий.
// desktop — локальный однопользовательский запуск (см. ServerInfo).
func NewAuthService(svc *auth.Service, images *agentimage.Service, runtime *runtimecfg.Service, desktop bool, version string, password bool, oidcName string, oidcLogin *auth.OIDC, secure bool) *AuthService {
	return &AuthService{svc: svc, images: images, runtime: runtime, desktop: desktop, version: version, password: password, oidcName: oidcName, oidc: oidcLogin, secure: secure}
}

// GetAgentRuntime возвращает режим исполнения сессий и доступные docker-контексты.
func (s *AuthService) GetAgentRuntime(_ context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.AgentRuntimeSettings], error) {
	state, err := s.runtime.State()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(runtimeToProto(state)), nil
}

// SetAgentRuntime задаёт режим и docker-контекст. Применяется перезапуском приложения —
// спавнер создаётся один раз на старте.
func (s *AuthService) SetAgentRuntime(_ context.Context, req *connect.Request[v1.SetAgentRuntimeRequest]) (*connect.Response[v1.AgentRuntimeSettings], error) {
	state, err := s.runtime.Set(req.Msg.Mode, req.Msg.DockerContext)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(runtimeToProto(state)), nil
}

func runtimeToProto(s runtimecfg.State) *v1.AgentRuntimeSettings {
	out := &v1.AgentRuntimeSettings{
		Mode:            s.Mode,
		RunningMode:     s.RunningMode,
		DockerContext:   s.DockerContext,
		RunningContext:  s.RunningContext,
		Editable:        s.Editable,
		RestartRequired: s.RestartRequired(),
		DockerError:     s.DockerError,
	}
	for _, c := range s.Contexts {
		out.Contexts = append(out.Contexts, &v1.DockerContext{Name: c.Name, Host: c.Host, Current: c.Current})
	}
	return out
}

// GetAgentImages возвращает образы контейнера агента текущего пользователя, базовый образ
// и состояние квоты.
func (s *AuthService) GetAgentImages(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.AgentImagesSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	settings, err := s.images.List(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(imagesToProto(settings)), nil
}

// SetAgentImages перезаписывает список образов пользователя, проверяя каждый на
// доступность, пригодность для сессий и вписываемость в квоту.
func (s *AuthService) SetAgentImages(ctx context.Context, req *connect.Request[v1.SetAgentImagesRequest]) (*connect.Response[v1.AgentImagesSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	settings, err := s.images.Set(ctx, u.ID, req.Msg.Images)
	if err != nil {
		switch {
		case errors.Is(err, agentimage.ErrUnavailable):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		case errors.Is(err, agentimage.ErrQuotaExceeded):
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(imagesToProto(settings)), nil
}

func imagesToProto(s agentimage.Settings) *v1.AgentImagesSettings {
	out := &v1.AgentImagesSettings{
		DefaultImage: s.DefaultImage,
		UsedBytes:    s.UsedBytes,
		QuotaBytes:   s.QuotaBytes,
	}
	for _, img := range s.Images {
		out.Images = append(out.Images, &v1.AgentImage{Image: img.Ref, SizeBytes: img.Bytes})
	}
	return out
}

// GetServerInfo сообщает клиенту режим работы сервера. Авторизация не требуется по сути
// вопроса, но метод проходит общий интерсептор — клиент зовёт его после проверки сессии.
func (s *AuthService) GetServerInfo(_ context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ServerInfo], error) {
	info := &v1.ServerInfo{
		Desktop:      s.desktop,
		Version:      s.version,
		Capabilities: []string{"workspace-rw-v1", "tcp-tunnel-v1"},
	}
	if s.password {
		info.AuthMethods = append(info.AuthMethods, &v1.AuthMethod{Id: "password", Kind: "password", Name: "Логин и пароль"})
	}
	if s.oidc != nil {
		info.AuthMethods = append(info.AuthMethods, &v1.AuthMethod{Id: "oidc", Kind: "oidc", Name: s.oidcName})
	}
	return connect.NewResponse(info), nil
}

// Login проверяет учётные данные, выпускает пару токенов и для web-клиента выставляет
// access-токен httpOnly-cookie (mobile использует access_token из тела как Bearer).
func (s *AuthService) Login(ctx context.Context, req *connect.Request[v1.LoginRequest]) (*connect.Response[v1.LoginResponse], error) {
	if !s.password {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("вход по паролю отключён"))
	}
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
	setAccessCookie(resp.Header(), pair.AccessToken, pair.AccessExpiresAt, s.secure)
	setRefreshCookie(resp.Header(), pair.RefreshToken, pair.RefreshExpiresAt, s.secure)
	return resp, nil
}

func (s *AuthService) ExchangeOIDC(_ context.Context, req *connect.Request[v1.ExchangeOIDCRequest]) (*connect.Response[v1.LoginResponse], error) {
	if s.oidc == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("OIDC отключён"))
	}
	pair, err := s.oidc.ExchangeHandoff(req.Msg.Code)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	return connect.NewResponse(loginResponse(pair)), nil
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
	setAccessCookie(resp.Header(), pair.AccessToken, pair.AccessExpiresAt, s.secure)
	setRefreshCookie(resp.Header(), pair.RefreshToken, pair.RefreshExpiresAt, s.secure)
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
	clearAccessCookie(resp.Header(), s.secure)
	clearRefreshCookie(resp.Header(), s.secure)
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

func (s *AuthService) GetCodexSettings(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.CodexSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	return s.codexSettings(ctx, u.ID)
}

func (s *AuthService) SetCodexApiKey(ctx context.Context, req *connect.Request[v1.SetCodexApiKeyRequest]) (*connect.Response[v1.CodexSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.SetCodexAPIKey(ctx, u.ID, strings.TrimSpace(req.Msg.ApiKey)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return s.codexSettings(ctx, u.ID)
}

func (s *AuthService) SetCodexChatGPTAuth(ctx context.Context, req *connect.Request[v1.SetCodexChatGPTAuthRequest]) (*connect.Response[v1.CodexSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.SetCodexAuthJSON(ctx, u.ID, strings.TrimSpace(req.Msg.AuthJson)); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return s.codexSettings(ctx, u.ID)
}

func (s *AuthService) StartCodexLogin(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.CodexLogin], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.codexLogin == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("device login недоступен в этой среде; импортируйте auth.json"))
	}
	return connect.NewResponse(codexLoginToProto(s.codexLogin.Start(u.ID))), nil
}

func (s *AuthService) GetCodexLogin(ctx context.Context, req *connect.Request[v1.GetCodexLoginRequest]) (*connect.Response[v1.CodexLogin], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.codexLogin == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("device login недоступен в этой среде"))
	}
	login, err := s.codexLogin.Get(u.ID, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(codexLoginToProto(login)), nil
}

func (s *AuthService) CancelCodexLogin(ctx context.Context, req *connect.Request[v1.CancelCodexLoginRequest]) (*connect.Response[v1.Empty], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if s.codexLogin == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("device login недоступен в этой среде"))
	}
	if err := s.codexLogin.Cancel(u.ID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func codexLoginToProto(login codexlogin.Login) *v1.CodexLogin {
	return &v1.CodexLogin{Id: login.ID, Status: login.Status, Output: login.Output, Error: login.Error}
}

func (s *AuthService) DisconnectCodexChatGPT(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.CodexSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.SetCodexAuthJSON(ctx, u.ID, ""); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return s.codexSettings(ctx, u.ID)
}

func (s *AuthService) SetCodexDefaultProfile(ctx context.Context, req *connect.Request[v1.SetCodexDefaultProfileRequest]) (*connect.Response[v1.CodexSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.svc.SetCodexDefaultProfile(ctx, u.ID, req.Msg.Profile); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return s.codexSettings(ctx, u.ID)
}

func (s *AuthService) codexSettings(ctx context.Context, userID string) (*connect.Response[v1.CodexSettings], error) {
	apiKeySet, connected, profile, err := s.svc.CodexSettings(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.CodexSettings{ApiKeySet: apiKeySet, ChatgptConnected: connected, DefaultProfile: profile}), nil
}

// GetMemorySettings возвращает git-remote личной памяти текущего пользователя.
func (s *AuthService) GetMemorySettings(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.MemorySettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	remote, err := s.svc.MemorySettings(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.MemorySettings{Remote: remote}), nil
}

// SetMemorySettings задаёт git-remote личной памяти текущего пользователя.
func (s *AuthService) SetMemorySettings(ctx context.Context, req *connect.Request[v1.SetMemorySettingsRequest]) (*connect.Response[v1.MemorySettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	remote := strings.TrimSpace(req.Msg.Remote)
	if err := s.svc.SetMemorySettings(ctx, u.ID, remote); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.MemorySettings{Remote: remote}), nil
}

// GetSSHSettings возвращает публичный SSH-ключ агента текущего пользователя (генерируя пару
// при первом обращении; приватный ключ не раскрывается).
func (s *AuthService) GetSSHSettings(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.SSHSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	_, pub, err := s.svc.EnsureAgentSSHKey(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SSHSettings{PublicKey: pub}), nil
}

// RegenerateSSHKey перевыпускает пару SSH-ключей агента текущего пользователя.
func (s *AuthService) RegenerateSSHKey(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.SSHSettings], error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	pub, err := s.svc.RegenerateAgentSSHKey(ctx, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SSHSettings{PublicKey: pub}), nil
}

// userToProto переводит доменного пользователя auth в proto-сообщение.
func userToProto(u auth.User) *v1.User {
	return &v1.User{Id: u.ID, Username: u.Username}
}

func loginResponse(pair auth.TokenPair) *v1.LoginResponse {
	return &v1.LoginResponse{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, User: userToProto(pair.User)}
}

func (s *AuthService) OIDCStartHandler(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	desktopCallback := r.URL.Query().Get("desktop_callback")
	if desktopCallback != "" && !validDesktopCallback(desktopCallback) {
		http.Error(w, "invalid desktop callback", http.StatusBadRequest)
		return
	}
	authorizationURL, err := s.oidc.Start(returnTo, desktopCallback)
	if err != nil {
		log.Printf("auth: start OIDC: %v", err)
		http.Error(w, "OIDC start failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (s *AuthService) OIDCCallbackHandler(w http.ResponseWriter, r *http.Request) {
	pair, returnTo, desktopCallback, err := s.oidc.Complete(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"))
	if err != nil {
		log.Printf("auth: complete OIDC: %v", err)
		if desktopCallback != "" {
			redirectWithQuery(w, r, desktopCallback, "error", "oidc")
			return
		}
		http.Redirect(w, r, "/login?error=oidc", http.StatusFound)
		return
	}
	if desktopCallback != "" {
		code, err := s.oidc.CreateHandoff(pair)
		if err != nil {
			redirectWithQuery(w, r, desktopCallback, "error", "oidc")
			return
		}
		redirectWithQuery(w, r, desktopCallback, "code", code)
		return
	}
	setAccessCookie(w.Header(), pair.AccessToken, pair.AccessExpiresAt, s.secure)
	setRefreshCookie(w.Header(), pair.RefreshToken, pair.RefreshExpiresAt, s.secure)
	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusFound)
}

func safeReturnTo(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || parsed.IsAbs() || parsed.Host != "" {
		return "/sessions"
	}
	return value
}

func validDesktopCallback(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && parsed.Host == "127.0.0.1:8787" && parsed.Path == "/desktop/oidc/callback" && parsed.User == nil && parsed.Fragment == ""
}

func redirectWithQuery(w http.ResponseWriter, r *http.Request, destination, key, value string) {
	parsed, _ := url.Parse(destination)
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	http.Redirect(w, r, parsed.String(), http.StatusFound)
}

// DesktopLoginHandler — HTTP-обработчик авто-логина для десктоп-режима: выпускает сессию
// сид-пользователя (без пароля) и ставит те же httpOnly-cookie, что и обычный Login, затем
// редиректит на SPA. Приложение локальное и однопользовательское (127.0.0.1), экран логина в
// нём — лишнее трение. Регистрируется ТОЛЬКО в десктоп-режиме; в серверном ручки нет.
func (s *AuthService) DesktopLoginHandler(username string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pair, err := s.svc.IssueForUser(r.Context(), username)
		if err != nil {
			http.Error(w, "desktop auto-login failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		setAccessCookie(w.Header(), pair.AccessToken, pair.AccessExpiresAt, false)
		setRefreshCookie(w.Header(), pair.RefreshToken, pair.RefreshExpiresAt, false)
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// setAccessCookie добавляет в ответ Set-Cookie с access-токеном (httpOnly) для web.
// SameSite=Lax и Path=/ покрывают и unary-вызовы, и WS-апгрейд того же origin.
func setAccessCookie(h http.Header, token string, expiresAt time.Time, secure bool) {
	c := &http.Cookie{
		Name:     auth.AccessCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// clearAccessCookie добавляет Set-Cookie, удаляющий access-cookie (logout).
func clearAccessCookie(h http.Header, secure bool) {
	c := &http.Cookie{
		Name:     auth.AccessCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// setRefreshCookie добавляет в ответ Set-Cookie с refresh-токеном (httpOnly) для web.
// Долгоживущая cookie (TTL = refresh_ttl) переживает перезагрузку страницы и закрытие
// вкладки, давая фронту тихо обновлять короткий access-токен через Refresh.
func setRefreshCookie(h http.Header, token string, expiresAt time.Time, secure bool) {
	c := &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}

// clearRefreshCookie добавляет Set-Cookie, удаляющий refresh-cookie (logout).
func clearRefreshCookie(h http.Header, secure bool) {
	c := &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	h.Add("Set-Cookie", c.String())
}
