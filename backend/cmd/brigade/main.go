// Команда brigade — точка входа сервиса: загрузка конфига, открытие SQLite,
// сборка доменов (auth, реестр сессий, спавнер) и подъём HTTP-сервера с
// ConnectRPC-хендлерами, WS-терминалом, AG-UI-транспортом (SSE) и встроенным фронтендом.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"

	"connectrpc.com/connect"

	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/config"
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

	// Спавнер выбирается по режиму конфига: local — pty в хост-процессе, docker —
	// контейнер на сессию.
	spawner, closeSpawner, err := buildSpawner(cfg.Mode)
	if err != nil {
		log.Fatalf("brigade: spawner: %v", err)
	}
	defer closeSpawner()

	// Реестр живых сессий поверх store и спавнера. mode передаётся строкой store.
	registry := session.NewRegistry(st, spawner, store.SessionMode(cfg.Mode), cfg.WorkDir, cfg.ClaudeCodeOAuthToken)
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
	mux.Handle(brigadev1connect.NewSessionServiceHandler(connectsvc.NewSessionService(registry, tickets), interceptors))
	mux.Handle(brigadev1connect.NewAgentServiceHandler(connectsvc.NewAgentService(), interceptors))

	// WS-терминал (Go 1.22 method+path routing). Аутентификация — по одноразовому
	// тикету в query; реестр отдаёт живой Handle сессии её владельцу. ACP-режим идёт не
	// по WS, а по каноническому AG-UI (SSE, см. ниже).
	mux.Handle("/ws/terminal/{sessionId}", termws.Handler(tickets, registry))

	// AG-UI (канонический protocol поверх SSE). В отличие от WS-режима аутентификация —
	// Bearer access-JWT на каждый запрос, а не одноразовый тикет; threadId трактуется как
	// идентификатор сессии. POST /api/ag-ui/{run,permission}.
	aguitransport.Mux(mux, jwtVerifier{jwt: authSvc.JWT()}, aguiProvider{registry: registry})

	// Встроенный SPA-фронтенд обслуживает все прочие пути.
	webHandler, err := web.Handler()
	if err != nil {
		log.Fatalf("brigade: web: %v", err)
	}
	mux.Handle("/", webHandler)

	// Кликабельный URL в логе: пустой/wildcard-хост в Addr (":10000", "0.0.0.0:…")
	// заменяется на localhost, чтобы ссылку можно было открыть из терминала.
	log.Printf("brigade: mode=%s, listening on %s", cfg.Mode, listenURL(cfg.Addr))
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.Fatalf("brigade: server: %v", err)
	}
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

// buildSpawner создаёт спавнер по режиму конфига и возвращает функцию его остановки
// (docker-клиент требует Close; local — no-op).
func buildSpawner(mode config.Mode) (spawn.Spawner, func(), error) {
	switch mode {
	case config.ModeDocker:
		ds, err := spawn.NewDockerSpawner()
		if err != nil {
			return nil, nil, err
		}
		return ds, func() { _ = ds.Close() }, nil
	default:
		return spawn.NewLocalSpawner(), func() {}, nil
	}
}
