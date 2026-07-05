package preview

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

// ipCacheTTL ограничивает возраст закэшированного IP контейнера: контейнер сессии
// может быть пересоздан (restore), и его IP изменится.
const ipCacheTTL = 30 * time.Second

// SessionSource отдаёт сессию по идентификатору. Удовлетворяется store.Store.
type SessionSource interface {
	GetSession(ctx context.Context, id string) (store.Session, error)
}

// ContainerIPs резолвит IP контейнера, обслуживающего сессию: собственный контейнер
// сессии (legacy CLI, ACP) либо общий per-user контейнер (shared CLI) — поэтому нужен
// и userID. Удовлетворяется spawn.DockerSpawner; nil в local-режиме сервиса.
type ContainerIPs interface {
	ContainerIP(ctx context.Context, sessionID, userID string) (string, error)
}

// Resolver превращает пару (sessionID, port) из hostname запроса в upstream-URL.
// Сессия валидируется через store (существует и running) — реестр живых объектов
// не нужен: ссылки публичные, а статус в store — источник истины.
type Resolver struct {
	sessions SessionSource
	docker   ContainerIPs

	mu  sync.Mutex
	ips map[string]ipEntry
}

type ipEntry struct {
	ip        string
	fetchedAt time.Time
}

// NewResolver собирает резолвер. docker может быть nil (local-режим сервиса).
func NewResolver(sessions SessionSource, docker ContainerIPs) *Resolver {
	return &Resolver{
		sessions: sessions,
		docker:   docker,
		ips:      make(map[string]ipEntry),
	}
}

// Resolve валидирует сессию и возвращает upstream-URL порта: local-сессия —
// 127.0.0.1 хоста, docker-сессия — IP контейнера в bridge-сети.
func (r *Resolver) Resolve(ctx context.Context, sessionID string, port int) (*url.URL, error) {
	sess, err := r.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("preview: session %s: %w", sessionID, err)
	}
	if sess.Status != store.SessionStatusRunning {
		return nil, fmt.Errorf("preview: session %s is not running (%s): %w", sessionID, sess.Status, store.ErrNotFound)
	}

	host := "127.0.0.1"
	if sess.Mode == store.SessionModeDocker {
		ip, err := r.containerIP(ctx, sessionID, sess.UserID)
		if err != nil {
			return nil, err
		}
		host = ip
	}
	return &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, port)}, nil
}

// containerIP возвращает IP контейнера сессии с кэшированием (TTL ipCacheTTL).
func (r *Resolver) containerIP(ctx context.Context, sessionID, userID string) (string, error) {
	if r.docker == nil {
		return "", fmt.Errorf("preview: docker session %s without docker spawner", sessionID)
	}

	r.mu.Lock()
	entry, ok := r.ips[sessionID]
	r.mu.Unlock()
	if ok && time.Since(entry.fetchedAt) < ipCacheTTL {
		return entry.ip, nil
	}

	ip, err := r.docker.ContainerIP(ctx, sessionID, userID)
	if err != nil {
		return "", fmt.Errorf("preview: container ip %s: %w", sessionID, err)
	}

	r.mu.Lock()
	r.ips[sessionID] = ipEntry{ip: ip, fetchedAt: time.Now()}
	r.mu.Unlock()
	return ip, nil
}

// Invalidate сбрасывает закэшированный IP сессии. Вызывается прокси при ошибке
// соединения с upstream: контейнер мог быть пересоздан с новым IP.
func (r *Resolver) Invalidate(sessionID string) {
	r.mu.Lock()
	delete(r.ips, sessionID)
	r.mu.Unlock()
}
