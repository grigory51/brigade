// Package preview публикует dev-серверы сессий через встроенный L7-прокси.
//
// Схема URL: {sessionId}-{port}.{domain} — порт и сессия кодируются в одном
// поддомен-лейбле (совместимо с одним wildcard-сертификатом *.domain). Маршрут
// выводится из hostname детерминированно, поэтому проксирование работает для
// любого порта без предварительной регистрации; регистрация агентом (Register)
// нужна только чтобы ссылка появилась в UI сессии.
//
// Аутентификация регистрации — детерминированный HMAC-токен сессии: он выводится
// из секрета и sessionID, не требует хранилища и переживает рестарт brigade.
package preview

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/grigory51/brigade/backend/internal/agentauth"
)

// tokenContext отделяет HMAC-токены preview от любых других применений того же
// секрета (например, подписи JWT).
const tokenContext = "brigade-preview:"

// Config — параметры публикации preview (снимок секции конфига + порт listener'а).
type Config struct {
	Enabled bool
	// Mode — subdomain (поддомены) | cookie (один хост + cookie-роутинг).
	Mode string
	// Domain — базовый домен preview-поддоменов (mode=subdomain).
	Domain string
	// CookieHost — хост cookie-режима целиком (mode=cookie).
	CookieHost string
	// Scheme — схема публичных URL (http|https).
	Scheme string
	// ExternalPort — порт публичных URL; 0 — использовать ListenPort.
	ExternalPort int
	// ListenPort — порт listener'а, обслуживающего публичные preview-URL
	// (plain из addr либо TLS-listener при scheme=https). Задаётся в main при сборке.
	ListenPort int
	// APIPort — порт plain-listener'а brigade для API регистрации preview: агент
	// обращается к нему по http (127.0.0.1 / host.docker.internal) независимо от
	// схемы публичных URL.
	APIPort int
}

// publicPort возвращает порт публичного URL с учётом ExternalPort.
func (c Config) publicPort() int {
	if c.ExternalPort > 0 {
		return c.ExternalPort
	}
	return c.ListenPort
}

// PublicURL строит публичный URL preview для порта сессии.
//   - subdomain: {scheme}://{id}-{port}.{domain}[:port];
//   - cookie:    {scheme}://{cookieHost}[:port]/?id={id}-{port}.
//
// Стандартные порты (80/http, 443/https) в URL опускаются.
func (c Config) PublicURL(sessionID string, port int) string {
	if c.Mode == "cookie" {
		base := c.schemeHost(c.CookieHost)
		return fmt.Sprintf("%s/?id=%s", base, cookieValue(sessionID, port))
	}
	host := fmt.Sprintf("%s-%d.%s", sessionID, port, c.Domain)
	return c.schemeHost(host)
}

// schemeHost собирает {scheme}://{host}[:port], опуская стандартный порт.
func (c Config) schemeHost(host string) string {
	p := c.publicPort()
	if (c.Scheme == "http" && p == 80) || (c.Scheme == "https" && p == 443) {
		return fmt.Sprintf("%s://%s", c.Scheme, host)
	}
	return fmt.Sprintf("%s://%s:%d", c.Scheme, host, p)
}

// cookieValue кодирует сессию и порт в значение cookie / query id (тот же формат,
// что поддомен-лейбл): {sessionId}-{port}.
func cookieValue(sessionID string, port int) string {
	return fmt.Sprintf("%s-%d", sessionID, port)
}

// URLTemplate возвращает шаблон публичного URL сессии с плейсхолдером {port}.
// Передаётся агенту в окружении: скилл подставляет фактический порт.
func (c Config) URLTemplate(sessionID string) string {
	// PublicURL с маркерным портом, затем маркер заменяется на плейсхолдер: логика
	// пропуска стандартных портов остаётся в одном месте.
	const marker = 65535
	return strings.Replace(c.PublicURL(sessionID, marker), fmt.Sprintf("-%d", marker), "-{port}", 1)
}

// Registered — зарегистрированный агентом preview-эндпоинт (для отображения в UI).
type Registered struct {
	Port int    `json:"port"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`
}

// Service — HMAC-токены регистрации и in-memory реестр preview по сессиям.
// Реестр не персистится: маршрут детерминирован и работает без регистрации,
// потеря ссылок в UI при рестарте исправляется повторной регистрацией.
type Service struct {
	cfg          Config
	secret       []byte
	daemonSigner *agentauth.Signer

	mu        sync.Mutex
	bySession map[string][]Registered
}

// NewService создаёт сервис preview. secret — общий секрет brigade (jwt.secret);
// контекст-префикс отделяет токены preview от JWT.
func NewService(cfg Config, secret []byte) *Service {
	return &Service{
		cfg:          cfg,
		secret:       secret,
		daemonSigner: agentauth.NewSigner(string(secret)),
		bySession:    make(map[string][]Registered),
	}
}

// Config возвращает конфигурацию публикации.
func (s *Service) Config() Config { return s.cfg }

// TokenFor возвращает регистрационный токен сессии: hex(HMAC-SHA256(secret, ctx+id)).
func (s *Service) TokenFor(sessionID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(tokenContext + sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyToken проверяет регистрационный токен сессии (constant-time).
func (s *Service) VerifyToken(sessionID, token string) bool {
	want := s.TokenFor(sessionID)
	return hmac.Equal([]byte(want), []byte(token))
}

// DaemonPublicKey — публичный Ed25519-ключ brigade (base64) для env контейнера сессии
// (BRIGADE_DAEMON_PUBKEY). Демон проверяет им подпись вызовов; утечка env импрсонацию не даёт.
func (s *Service) DaemonPublicKey() string {
	return s.daemonSigner.PublicKeyB64()
}

// DaemonToken подписывает короткоживущий токен для вызова демона сессии (aud=sessionID).
// brigade подписывает приватным ключом на каждый запрос; демон проверяет публичным.
func (s *Service) DaemonToken(sessionID string) (string, error) {
	return s.daemonSigner.Token(sessionID)
}

// Register фиксирует preview-эндпоинт сессии (upsert по порту) и возвращает запись
// с публичным URL.
func (s *Service) Register(sessionID string, port int, name string) Registered {
	reg := Registered{
		Port: port,
		Name: name,
		URL:  s.cfg.PublicURL(sessionID, port),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.bySession[sessionID]
	replaced := false
	for i := range list {
		if list[i].Port == port {
			list[i] = reg
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, reg)
		sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
	}
	s.bySession[sessionID] = list
	return reg
}

// List возвращает копию зарегистрированных preview сессии (по возрастанию порта).
func (s *Service) List(sessionID string) []Registered {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.bySession[sessionID]
	out := make([]Registered, len(list))
	copy(out, list)
	return out
}

// Drop забывает preview сессии. Вызывается при Stop/Delete сессии.
func (s *Service) Drop(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, sessionID)
}

// SplitHostPort отделяет порт от хоста запроса, не считая его отсутствие ошибкой.
func SplitHostPort(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}
