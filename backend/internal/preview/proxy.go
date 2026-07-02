package preview

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/grigory51/brigade/backend/internal/store"
)

// hostLabelRe разбирает preview-лейбл поддомена: {uuid сессии}-{port}. Формат UUID
// фиксирован, поэтому дефисы внутри идентификатора не конфликтуют с разделителем порта.
var hostLabelRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})-([0-9]{1,5})$`)

// Wrap оборачивает основной handler brigade preview-прокси: запросы на хосты
// {sessionId}-{port}.{domain} проксируются к dev-серверу сессии, остальные уходят
// в next. При выключенном preview возвращает next без обёртки.
func Wrap(cfg Config, resolver *Resolver, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}

	suffix := "." + strings.ToLower(cfg.Domain)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(SplitHostPort(r.Host))
		label, ok := strings.CutSuffix(host, suffix)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		m := hostLabelRe.FindStringSubmatch(label)
		if m == nil {
			next.ServeHTTP(w, r)
			return
		}
		sessionID := m[1]
		port, err := strconv.Atoi(m[2])
		if err != nil || port < 1 || port > 65535 {
			http.Error(w, "invalid preview port", http.StatusBadRequest)
			return
		}

		target, err := resolver.Resolve(r.Context(), sessionID, port)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "preview: session not found or not running", http.StatusNotFound)
				return
			}
			log.Printf("preview: resolve %s port %d: %v", sessionID, port, err)
			http.Error(w, "preview: upstream unavailable", http.StatusBadGateway)
			return
		}

		proxyTo(target, resolver, sessionID).ServeHTTP(w, r)
	})
}

// proxyTo собирает ReverseProxy до upstream'а сессии. WebSocket-upgrade httputil
// обрабатывает сам; FlushInterval < 0 отключает буферизацию ответа (SSE, стриминг).
func proxyTo(target *url.URL, resolver *Resolver, sessionID string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			// Dev-серверы с проверкой Host (vite allowedHosts и т. п.) должны видеть
			// адрес, на котором реально слушают, а не публичный preview-домен.
			pr.Out.Host = target.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// Контейнер мог быть пересоздан с новым IP — сбрасываем кэш, чтобы
			// следующий запрос перечитал его.
			resolver.Invalidate(sessionID)
			log.Printf("preview: proxy %s -> %s: %v", sessionID, target, err)
			http.Error(w, "preview: dev server unreachable", http.StatusBadGateway)
		},
	}
}
