// Команда brigade — точка входа сервиса: загрузка конфига, открытие SQLite,
// сборка доменов (auth, реестр сессий, спавнер) и подъём HTTP-сервера с
// ConnectRPC-хендлерами, WS-терминалом, AG-UI-транспортом (SSE) и встроенным фронтендом.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"connectrpc.com/connect"

	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/config"
	"github.com/grigory51/brigade/backend/internal/preview"
	"github.com/grigory51/brigade/backend/internal/session"
	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
	aguitransport "github.com/grigory51/brigade/backend/internal/transport/agui"
	connectsvc "github.com/grigory51/brigade/backend/internal/transport/connect"
	"github.com/grigory51/brigade/backend/internal/transport/termws"
	"github.com/grigory51/brigade/backend/internal/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("brigade: config: %v", err)
	}

	st, err := store.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("brigade: store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Авторизация: сервис JWT/refresh, сидирование стартового пользователя и
	// процессное хранилище одноразовых WS-тикетов.
	authSvc := auth.NewService(st.DB(), cfg.JWT.Secret, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL)
	if err := authSvc.EnsureSeedUser(ctx, cfg.Seed.Username, cfg.Seed.Password); err != nil {
		log.Fatalf("brigade: seed user: %v", err)
	}
	tickets := auth.NewTicketStore()

	// Спавнер выбирается по режиму инстанса (BRIGADE_MODE): local — pty в
	// хост-процессе, docker — контейнер на сессию. Все сессии наследуют этот режим.
	spawner, closeSpawner, err := buildSpawner(cfg.Mode)
	if err != nil {
		log.Fatalf("brigade: spawner: %v", err)
	}
	defer closeSpawner()

	// Публикация dev-серверов сессий: HMAC-токены регистрации, реестр preview и
	// конфигурация публичных URL. Сервис создаётся всегда (даже при выключенном
	// preview) — зависимые компоненты не проверяют nil.
	previewSvc := preview.NewService(previewConfig(cfg), []byte(cfg.JWT.Secret))

	// Реестр живых сессий поверх store и спавнера. Режим фиксируется в каждой сессии;
	// подписочный токен Claude берётся per-user из store при создании сессии.
	registry := session.NewRegistry(st, spawner, store.SessionMode(cfg.Mode), cfg.WorkDir, cfg.ClaudeHomeDir, previewSvc)
	defer registry.Close()

	// Восстановление живых сессий после рестарта: упавшие помечаются failed и не
	// прерывают старт.
	if err := registry.RestoreAll(ctx); err != nil {
		log.Fatalf("brigade: restore sessions: %v", err)
	}

	mux := http.NewServeMux()

	// ConnectRPC-хендлеры. auth.Interceptor кладёт пользователя в контекст из Bearer
	// или cookie; обязательность авторизации проверяет сам хендлер.
	interceptors := connect.WithInterceptors(authSvc.Interceptor())
	mux.Handle(brigadev1connect.NewAuthServiceHandler(connectsvc.NewAuthService(authSvc), interceptors))
	mux.Handle(brigadev1connect.NewSessionServiceHandler(connectsvc.NewSessionService(registry, tickets, previewSvc), interceptors))
	mux.Handle(brigadev1connect.NewAgentServiceHandler(connectsvc.NewAgentService(), interceptors))

	// WS-терминал (Go 1.22 method+path routing). Аутентификация — по одноразовому
	// тикету в query; реестр отдаёт живой Handle сессии её владельцу. ACP-режим идёт не
	// по WS, а по каноническому AG-UI (SSE, см. ниже).
	mux.Handle("/ws/terminal/{sessionId}", termws.Handler(tickets, registry))

	// WS вспомогательного шелла: параллельный терминал рядом с любой сессией (осмотр
	// рабочей директории руками). Шелл спавнится на подключение и живёт до его разрыва.
	mux.Handle("/ws/shell/{sessionId}", termws.ShellHandler(tickets, registry))

	// AG-UI (канонический protocol поверх SSE). В отличие от WS-режима аутентификация —
	// Bearer access-JWT на каждый запрос, а не одноразовый тикет; threadId трактуется как
	// идентификатор сессии. POST /api/ag-ui/{run,permission}.
	aguitransport.Mux(mux, jwtVerifier{jwt: authSvc.JWT()}, aguiProvider{registry: registry})

	// Регистрация preview агентом (curl из скилла): Bearer — HMAC-токен сессии из
	// окружения агента, не JWT пользователя.
	mux.Handle("POST /api/preview/{sessionId}/register", previewSvc.RegisterHandler())

	// Встроенный SPA-фронтенд обслуживает все прочие пути.
	webHandler, err := web.Handler()
	if err != nil {
		log.Fatalf("brigade: web: %v", err)
	}
	mux.Handle("/", webHandler)

	// Preview-прокси оборачивает весь сервер: запросы на {sessionId}-{port}.{domain}
	// проксируются к dev-серверу сессии, остальные обслуживает mux.
	var handler http.Handler = mux
	if cfg.Preview.Enabled {
		var ips preview.ContainerIPs
		if ds, ok := spawner.(*spawn.DockerSpawner); ok {
			ips = ds
		}
		handler = preview.Wrap(previewSvc.Config(), preview.NewResolver(st, ips), mux)
	}

	// Встроенная TLS-терминация: отдельный HTTPS-listener с тем же handler'ом.
	// Plain-listener на Addr продолжает работать (локальный доступ, API для агентов).
	if cfg.TLS.Addr != "" {
		go func() {
			log.Printf("brigade: tls listening on %s", cfg.TLS.Addr)
			if err := http.ListenAndServeTLS(cfg.TLS.Addr, cfg.TLS.CertFile, cfg.TLS.KeyFile, handler); err != nil {
				log.Fatalf("brigade: tls server: %v", err)
			}
		}()
	}

	// Кликабельный URL в логе: пустой/wildcard-хост в Addr (":10000", "0.0.0.0:…")
	// заменяется на localhost, чтобы ссылку можно было открыть из терминала.
	log.Printf("brigade: mode=%s, listening on %s", cfg.Mode, listenURL(cfg.Addr))
	if err := http.ListenAndServe(cfg.Addr, handler); err != nil {
		log.Fatalf("brigade: server: %v", err)
	}
}

// buildSpawner создаёт спавнер по режиму инстанса и возвращает функцию его остановки
// (docker-клиент требует Close; local — no-op). Docker-режим проверяет достижимость
// демона (Ping): недоступен → фатально, инстанс с mode=docker без docker бессмыслен.
func buildSpawner(mode config.Mode) (spawn.Spawner, func(), error) {
	switch mode {
	case config.ModeDocker:
		ds, err := spawn.NewDockerSpawner()
		if err != nil {
			return nil, nil, err
		}
		if err := ds.Ping(context.Background()); err != nil {
			_ = ds.Close()
			return nil, nil, fmt.Errorf("docker daemon unreachable: %w", err)
		}
		return ds, func() { _ = ds.Close() }, nil
	default:
		return spawn.NewLocalSpawner(), func() {}, nil
	}
}

// previewConfig собирает preview.Config из секции конфига, дополняя её фактическим
// портом listener'а: при https со встроенным TLS публичные URL указывают на
// TLS-listener, иначе — на plain-listener из Addr.
func previewConfig(cfg *config.Config) preview.Config {
	pc := preview.Config{
		Enabled:      cfg.Preview.Enabled,
		Mode:         cfg.Preview.Mode,
		Domain:       cfg.Preview.Domain,
		CookieHost:   cfg.Preview.CookieHost,
		Scheme:       cfg.Preview.Scheme,
		ExternalPort: cfg.Preview.ExternalPort,
		ListenPort:   addrPort(cfg.Addr),
		APIPort:      addrPort(cfg.Addr),
	}
	if pc.Scheme == "https" && cfg.TLS.Addr != "" {
		pc.ListenPort = addrPort(cfg.TLS.Addr)
	}
	return pc
}

// addrPort извлекает числовой порт из адреса вида "host:port" или ":port".
func addrPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// listenURL строит человекочитаемый http-URL из адреса прослушивания. Хост,
// непригодный для открытия в браузере (пустой или wildcard), заменяется на
// localhost; порт сохраняется.
func listenURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// jwtVerifier адаптирует *auth.JWT к aguitransport.TokenVerifier: проверяет access-JWT и
// отдаёт идентификатор пользователя (Subject) при валидном токене.
type jwtVerifier struct{ jwt *auth.JWT }

func (v jwtVerifier) Verify(token string) (userID string, ok bool) {
	claims, err := v.jwt.Verify(token)
	if err != nil {
		return "", false
	}
	return claims.Subject, true
}

// aguiProvider адаптирует реестр сессий к aguitransport.ClientProvider: AG-UI-транспорту
// нужен живой *acp.Client сессии, который удовлетворяет его интерфейс Bindable напрямую.
type aguiProvider struct{ registry *session.Registry }

func (p aguiProvider) Bindable(sessionID, userID string) (aguitransport.Bindable, bool) {
	c, ok := p.registry.ACPClient(sessionID, userID)
	if !ok {
		return nil, false
	}
	return c, true
}
