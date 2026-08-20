package desktopenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

const LocalID = "local"

type Environment struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	BaseURL      string           `json:"base_url,omitempty"`
	Username     string           `json:"username,omitempty"`
	Version      string           `json:"version,omitempty"`
	Capabilities []string         `json:"capabilities,omitempty"`
	AuthMethods  []*v1.AuthMethod `json:"auth_methods,omitempty"`
	PortForwards []PortForward    `json:"port_forwards,omitempty"`
	Mounts       []Mount          `json:"mounts,omitempty"`
	Error        string           `json:"-"`
}

type config struct {
	ActiveID     string         `json:"active_id"`
	NeedsSetup   bool           `json:"needs_setup,omitempty"`
	Environments []*Environment `json:"environments"`
}

type tokenStore interface {
	Get(id string) (string, error)
	Set(id, token string) error
	Delete(id string) error
}

type tokenState struct {
	access  string
	expires time.Time
}

type Manager struct {
	mu               sync.Mutex
	tokenMu          sync.Mutex
	path             string
	config           config
	tokens           tokenStore
	access           map[string]tokenState
	http             *http.Client
	forwardListeners map[string]net.Listener
	resourceErrors   map[string]string
	mountHandles     map[string]mountHandle
	resourceContext  context.Context
	resourceCancel   context.CancelFunc
}

func New(path string, needsSetup bool) (*Manager, error) {
	resourceContext, resourceCancel := context.WithCancel(context.Background())
	m := &Manager{path: path, tokens: newTokenStore(path), access: map[string]tokenState{}, http: &http.Client{Timeout: 30 * time.Second}, forwardListeners: map[string]net.Listener{}, resourceErrors: map[string]string{}, mountHandles: map[string]mountHandle{}, resourceContext: resourceContext, resourceCancel: resourceCancel}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &m.config); err != nil {
			return nil, fmt.Errorf("desktop environments: decode: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(m.config.Environments) == 0 {
		m.config = config{ActiveID: LocalID, NeedsSetup: needsSetup, Environments: []*Environment{{ID: LocalID, Name: "Локальный", Kind: "local", Capabilities: []string{"workspace-rw-v1", "tcp-tunnel-v1"}}}}
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	if m.findLocked(m.config.ActiveID) == nil {
		m.config.ActiveID = LocalID
	}
	return m, nil
}

func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manager) findLocked(id string) *Environment {
	for _, environment := range m.config.Environments {
		if environment.ID == id {
			return environment
		}
	}
	return nil
}

func (m *Manager) List() ([]Environment, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Environment, 0, len(m.config.Environments))
	for _, environment := range m.config.Environments {
		out = append(out, *environment)
	}
	return out, m.config.ActiveID, m.config.NeedsSetup
}

// RefreshInfo синхронизирует способы входа после изменения конфигурации remote-инстанса.
func (m *Manager) RefreshInfo(ctx context.Context) {
	m.mu.Lock()
	remotes := make(map[string]string)
	for _, environment := range m.config.Environments {
		if environment.Kind == "remote" {
			remotes[environment.ID] = environment.BaseURL
		}
	}
	m.mu.Unlock()
	var wait sync.WaitGroup
	for id, baseURL := range remotes {
		wait.Add(1)
		go func() {
			defer wait.Done()
			info, err := m.serverInfo(ctx, baseURL)
			m.mu.Lock()
			if environment := m.findLocked(id); environment != nil {
				if err != nil {
					environment.AuthMethods = nil
					environment.Error = err.Error()
				} else {
					environment.AuthMethods = info.AuthMethods
					environment.Version = info.Version
					environment.Capabilities = info.Capabilities
					environment.Error = ""
				}
				_ = m.saveLocked()
			}
			m.mu.Unlock()
		}()
	}
	wait.Wait()
}

func normalizeRemoteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("некорректный URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("укажите origin без пути, query и credentials")
	}
	host := strings.ToLower(u.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("удалённое окружение требует HTTPS")
	}
	u.Path = ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (m *Manager) Add(ctx context.Context, name, rawURL string) (Environment, error) {
	baseURL, err := normalizeRemoteURL(rawURL)
	if err != nil {
		return Environment{}, err
	}
	info, err := m.serverInfo(ctx, baseURL)
	if err != nil {
		return Environment{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = infoName(baseURL)
	}
	environment := &Environment{ID: uuid.NewString(), Name: strings.TrimSpace(name), Kind: "remote", BaseURL: baseURL, Version: info.Version, Capabilities: info.Capabilities, AuthMethods: info.AuthMethods}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Environments = append(m.config.Environments, environment)
	if err := m.saveLocked(); err != nil {
		return Environment{}, err
	}
	return *environment, nil
}

func infoName(baseURL string) string { u, _ := url.Parse(baseURL); return u.Host }

func (m *Manager) Update(ctx context.Context, id, name, rawURL string) (Environment, error) {
	m.mu.Lock()
	current := m.findLocked(id)
	if current == nil || current.Kind == "local" {
		m.mu.Unlock()
		return Environment{}, errors.New("окружение не найдено")
	}
	oldURL := current.BaseURL
	m.mu.Unlock()
	baseURL, err := normalizeRemoteURL(rawURL)
	if err != nil {
		return Environment{}, err
	}
	info, err := m.serverInfo(ctx, baseURL)
	if err != nil {
		return Environment{}, err
	}
	m.mu.Lock()
	current = m.findLocked(id)
	if current == nil {
		m.mu.Unlock()
		return Environment{}, errors.New("окружение не найдено")
	}
	current.Name, current.BaseURL, current.Version, current.Capabilities, current.AuthMethods = strings.TrimSpace(name), baseURL, info.Version, info.Capabilities, info.AuthMethods
	if current.Name == "" {
		current.Name = infoName(baseURL)
	}
	if oldURL != baseURL {
		current.Username = ""
		delete(m.access, id)
		_ = m.tokens.Delete(id)
	}
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return Environment{}, err
	}
	var listeners []net.Listener
	var handles []mountHandle
	restoreResources := oldURL != baseURL && m.config.ActiveID == id
	if restoreResources {
		listeners, handles = m.detachResourcesLocked()
	}
	result := *current
	m.mu.Unlock()
	closeResources(listeners, handles)
	if restoreResources {
		go m.Restore(context.Background())
	}
	return result, nil
}

func (m *Manager) Delete(id string) error {
	if id == LocalID {
		return errors.New("локальное окружение нельзя удалить")
	}
	m.mu.Lock()
	index := -1
	for i, environment := range m.config.Environments {
		if environment.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		m.mu.Unlock()
		return errors.New("окружение не найдено")
	}
	active := m.config.ActiveID == id
	if m.config.ActiveID == id {
		m.config.ActiveID = LocalID
	}
	m.config.Environments = append(m.config.Environments[:index], m.config.Environments[index+1:]...)
	delete(m.access, id)
	if err := m.tokens.Delete(id); err != nil {
		m.mu.Unlock()
		return err
	}
	err := m.saveLocked()
	var listeners []net.Listener
	var handles []mountHandle
	if active && err == nil {
		listeners, handles = m.detachResourcesLocked()
	}
	m.mu.Unlock()
	closeResources(listeners, handles)
	return err
}

func (m *Manager) Select(id string) (Environment, error) {
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil {
		m.mu.Unlock()
		return Environment{}, errors.New("окружение не найдено")
	}
	m.config.ActiveID = id
	m.config.NeedsSetup = false
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return Environment{}, err
	}
	listeners, handles := m.detachResourcesLocked()
	result := *environment
	m.mu.Unlock()
	closeResources(listeners, handles)
	go m.Restore(context.Background())
	return result, nil
}

func (m *Manager) Login(ctx context.Context, id, username, password string) (Environment, error) {
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return Environment{}, errors.New("удалённое окружение не найдено")
	}
	baseURL := environment.BaseURL
	environment.Error = ""
	_ = m.saveLocked()
	m.mu.Unlock()
	client := brigadev1connect.NewAuthServiceClient(m.http, baseURL)
	response, err := client.Login(ctx, connect.NewRequest(&v1.LoginRequest{Username: username, Password: password}))
	if err != nil {
		return Environment{}, err
	}
	return m.storeLogin(id, response.Msg)
}

func (m *Manager) storeLogin(id string, login *v1.LoginResponse) (Environment, error) {
	if err := m.tokens.Set(id, login.RefreshToken); err != nil {
		return Environment{}, fmt.Errorf("keychain: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		return Environment{}, errors.New("удалённое окружение не найдено")
	}
	environment.Username = login.User.GetUsername()
	environment.Error = ""
	m.access[id] = tokenState{access: login.AccessToken, expires: tokenExpiry(login.AccessToken)}
	if err := m.saveLocked(); err != nil {
		return Environment{}, err
	}
	return *environment, nil
}

func (m *Manager) OIDCStartHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("environment_id")
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}
	baseURL := environment.BaseURL
	m.mu.Unlock()

	callback := &url.URL{Scheme: "http", Host: "127.0.0.1:8787", Path: "/desktop/oidc/callback"}
	callbackQuery := callback.Query()
	callbackQuery.Set("environment_id", id)
	callbackQuery.Set("return_to", safeLocalReturn(r.URL.Query().Get("return_to")))
	callback.RawQuery = callbackQuery.Encode()
	start, _ := url.Parse(baseURL + "/auth/oidc/start")
	query := start.Query()
	query.Set("desktop_callback", callback.String())
	start.RawQuery = query.Encode()
	http.Redirect(w, r, start.String(), http.StatusFound)
}

func (m *Manager) OIDCCallbackHandler(w http.ResponseWriter, r *http.Request) {
	returnTo := safeLocalReturn(r.URL.Query().Get("return_to"))
	if r.URL.Query().Get("error") != "" {
		id := r.URL.Query().Get("environment_id")
		m.mu.Lock()
		if environment := m.findLocked(id); environment != nil {
			environment.Error = "Вход через OIDC отменён или завершился ошибкой."
			_ = m.saveLocked()
		}
		m.mu.Unlock()
		if returnTo == "/desktop/oidc/done" {
			http.Redirect(w, r, returnTo+"?error=oidc", http.StatusFound)
		} else {
			http.Redirect(w, r, "/login?error=oidc", http.StatusFound)
		}
		return
	}
	id := r.URL.Query().Get("environment_id")
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}
	baseURL := environment.BaseURL
	m.mu.Unlock()
	client := brigadev1connect.NewAuthServiceClient(m.http, baseURL)
	response, err := client.ExchangeOIDC(r.Context(), connect.NewRequest(&v1.ExchangeOIDCRequest{Code: r.URL.Query().Get("code")}))
	if err != nil {
		log.Printf("desktop environments: exchange OIDC: %v", err)
		http.Redirect(w, r, "/login?error=oidc", http.StatusFound)
		return
	}
	if _, err := m.storeLogin(id, response.Msg); err != nil {
		log.Printf("desktop environments: store OIDC login: %v", err)
		http.Error(w, "store OIDC login failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (m *Manager) OIDCDoneHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	message := "Вход выполнен. Вернитесь в Brigade.app."
	if r.URL.Query().Get("error") != "" {
		message = "Вход отменён. Вернитесь в Brigade.app."
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Brigade</title><style>body{margin:0;display:grid;min-height:100vh;place-items:center;background:#1f1e1d;color:#f1eee8;font:16px system-ui}</style><p>%s</p>`, message)
}

func safeLocalReturn(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || parsed.IsAbs() || parsed.Host != "" {
		return "/sessions"
	}
	return value
}

func (m *Manager) Logout(id string) (Environment, error) {
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil {
		m.mu.Unlock()
		return Environment{}, errors.New("окружение не найдено")
	}
	delete(m.access, id)
	environment.Username = ""
	if err := m.tokens.Delete(id); err != nil {
		m.mu.Unlock()
		return Environment{}, err
	}
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return Environment{}, err
	}
	var listeners []net.Listener
	var handles []mountHandle
	if m.config.ActiveID == id {
		listeners, handles = m.detachResourcesLocked()
	}
	result := *environment
	m.mu.Unlock()
	closeResources(listeners, handles)
	return result, nil
}

func tokenExpiry(raw string) time.Time {
	claims := jwt.MapClaims{}
	_, _, _ = jwt.NewParser().ParseUnverified(raw, claims)
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		return exp.Time
	}
	return time.Now().Add(5 * time.Minute)
}

func (m *Manager) serverInfo(ctx context.Context, baseURL string) (*v1.ServerInfo, error) {
	client := brigadev1connect.NewAuthServiceClient(m.http, baseURL)
	response, err := client.GetServerInfo(ctx, connect.NewRequest(&v1.Empty{}))
	if err != nil {
		return nil, fmt.Errorf("подключение к %s: %w", baseURL, err)
	}
	if len(response.Msg.AuthMethods) == 0 {
		response.Msg.AuthMethods = []*v1.AuthMethod{{Id: "password", Kind: "password", Name: "Логин и пароль"}}
	}
	return response.Msg, nil
}

func (m *Manager) activeToken(ctx context.Context) (*Environment, string, error) {
	m.mu.Lock()
	id := m.config.ActiveID
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return nil, "", nil
	}
	m.mu.Unlock()
	return m.tokenFor(ctx, id)
}

func (m *Manager) tokenFor(ctx context.Context, id string) (*Environment, string, error) {
	m.mu.Lock()
	environment := m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return nil, "", errors.New("удалённое окружение не найдено")
	}
	copy := *environment
	state := m.access[environment.ID]
	m.mu.Unlock()
	if state.access != "" && time.Until(state.expires) > time.Minute {
		return &copy, state.access, nil
	}
	m.tokenMu.Lock()
	defer m.tokenMu.Unlock()
	m.mu.Lock()
	environment = m.findLocked(id)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return nil, "", errors.New("удалённое окружение не найдено")
	}
	copy = *environment
	state = m.access[id]
	m.mu.Unlock()
	if state.access != "" && time.Until(state.expires) > time.Minute {
		return &copy, state.access, nil
	}
	refresh, err := m.tokens.Get(copy.ID)
	if err != nil {
		return &copy, "", err
	}
	if refresh == "" {
		return &copy, "", errors.New("требуется вход")
	}
	client := brigadev1connect.NewAuthServiceClient(m.http, copy.BaseURL)
	response, err := client.Refresh(ctx, connect.NewRequest(&v1.RefreshRequest{RefreshToken: refresh}))
	if err != nil {
		return &copy, "", err
	}
	if err := m.tokens.Set(copy.ID, response.Msg.RefreshToken); err != nil {
		return &copy, "", err
	}
	state = tokenState{access: response.Msg.AccessToken, expires: tokenExpiry(response.Msg.AccessToken)}
	m.mu.Lock()
	m.access[copy.ID] = state
	m.mu.Unlock()
	return &copy, state.access, nil
}

func shouldProxy(path string) bool {
	if strings.HasPrefix(path, "/brigade.v1.DesktopService/") {
		return false
	}
	return strings.HasPrefix(path, "/brigade.v1.") || strings.HasPrefix(path, "/api/ag-ui/") || strings.HasPrefix(path, "/api/sessions/") || strings.HasPrefix(path, "/api/plugins/") || strings.HasPrefix(path, "/ws/")
}

// Proxy отправляет API и stream-запросы в активное remote-окружение, оставляя SPA и
// DesktopService локальными.
func (m *Manager) Proxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldProxy(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		environment, token, err := m.activeToken(r.Context())
		if environment == nil {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		target, _ := url.Parse(environment.BaseURL)
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		proxy.Director = func(req *http.Request) {
			original(req)
			req.Host = target.Host
			req.Header.Del("Cookie")
			req.Header.Set("Authorization", "Bearer "+token)
		}
		proxy.ModifyResponse = func(response *http.Response) error { response.Header.Del("Set-Cookie"); return nil }
		proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, proxyErr error) {
			http.Error(rw, proxyErr.Error(), http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
}
