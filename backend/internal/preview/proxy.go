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

// hostLabelRe разбирает preview-лейбл {uuid сессии}-{port} (поддомен-лейбл либо
// значение cookie/query id). Формат UUID фиксирован, дефисы внутри идентификатора не
// конфликтуют с разделителем порта.
var hostLabelRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})-([0-9]{1,5})$`)

// cookieName — имя cookie, несущей выбранный preview (значение {sessionId}-{port}).
const cookieName = "brigade_preview"

// parseIDPort разбирает лейбл {sessionId}-{port} в sessionID и порт. ok=false при
// несоответствии формату или недопустимом порте.
func parseIDPort(label string) (sessionID string, port int, ok bool) {
	m := hostLabelRe.FindStringSubmatch(label)
	if m == nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(m[2])
	if err != nil || p < 1 || p > 65535 {
		return "", 0, false
	}
	return m[1], p, true
}

// Wrap оборачивает основной handler brigade preview-прокси. При выключенном preview
// возвращает next без обёртки. Режим адресации — cfg.Mode: subdomain (по хосту) или
// cookie (по cookie на выделенном хосте).
func Wrap(cfg Config, resolver *Resolver, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}
	if cfg.Mode == "cookie" {
		return wrapCookie(cfg, resolver, next)
	}
	return wrapSubdomain(cfg, resolver, next)
}

// wrapSubdomain — поддоменный режим: запрос на {sessionId}-{port}.{domain}
// проксируется к dev-серверу сессии, остальные уходят в next.
func wrapSubdomain(cfg Config, resolver *Resolver, next http.Handler) http.Handler {
	suffix := "." + strings.ToLower(cfg.Domain)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(SplitHostPort(r.Host))
		label, ok := strings.CutSuffix(host, suffix)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		sessionID, port, ok := parseIDPort(label)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		proxyResolved(w, r, cfg, resolver, sessionID, port)
	})
}

// wrapCookie — cookie-режим на выделенном хосте CookieHost:
//   - не preview-хост → next (UI brigade);
//   - ?id=<id>-<port> → ставит cookie brigade_preview и 302-редирект на "/";
//   - иначе cookie brigade_preview → проксирование к dev-серверу (все пути, включая
//     корневые /assets/... — абсолютные пути dev-сервера работают);
//   - нет ни query, ни cookie → страница-заглушка «preview не выбран».
func wrapCookie(cfg Config, resolver *Resolver, next http.Handler) http.Handler {
	cookieHost := strings.ToLower(cfg.CookieHost)
	secure := cfg.Scheme == "https"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(SplitHostPort(r.Host)) != cookieHost {
			next.ServeHTTP(w, r)
			return
		}

		// Выбор preview: ?id=<id>-<port> → cookie + редирект на "/" без query, чтобы
		// последующие запросы (в т.ч. абсолютные ассеты) шли уже по cookie.
		if id := r.URL.Query().Get("id"); id != "" {
			if _, _, ok := parseIDPort(id); !ok {
				http.Error(w, "preview: invalid id", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    id,
				Path:     "/",
				SameSite: http.SameSiteLaxMode,
				Secure:   secure,
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" {
			previewNotSelected(w)
			return
		}
		sessionID, port, ok := parseIDPort(c.Value)
		if !ok {
			clearPreviewCookie(w, secure)
			previewNotSelected(w)
			return
		}
		proxyResolved(w, r, cfg, resolver, sessionID, port)
	})
}

// proxyResolved резолвит upstream сессии и проксирует запрос. Ошибки: 404 (сессия не
// найдена/не running) — в cookie-режиме дополнительно чистит cookie; 502 (upstream).
func proxyResolved(w http.ResponseWriter, r *http.Request, cfg Config, resolver *Resolver, sessionID string, port int) {
	target, err := resolver.Resolve(r.Context(), sessionID, port)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if cfg.Mode == "cookie" {
				clearPreviewCookie(w, cfg.Scheme == "https")
			}
			http.Error(w, "preview: session not found or not running", http.StatusNotFound)
			return
		}
		log.Printf("preview: resolve %s port %d: %v", sessionID, port, err)
		http.Error(w, "preview: upstream unavailable", http.StatusBadGateway)
		return
	}
	proxyTo(target, resolver, sessionID).ServeHTTP(w, r)
}

// clearPreviewCookie удаляет cookie выбора preview.
func clearPreviewCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// previewNotSelected отдаёт минимальную страницу-заглушку, когда preview не выбран.
func previewNotSelected(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>brigade preview</title>` +
		`<body style="font-family:system-ui;padding:2rem"><h1>Preview не выбран</h1>` +
		`<p>Откройте ссылку preview из сессии (она задаёт нужный dev-сервер).</p></body>`))
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
