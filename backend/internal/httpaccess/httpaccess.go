// Package httpaccess логирует начало и завершение входящих HTTP-запросов.
package httpaccess

import (
	"log"
	"net/http"
	"sync/atomic"

	"github.com/felixge/httpsnoop"
)

// Wrap добавляет access log, не включая query string и чувствительные заголовки.
func Wrap(component string, next http.Handler) http.Handler {
	return wrap(log.Default(), component, next)
}

func wrap(logger *log.Logger, component string, next http.Handler) http.Handler {
	var sequence atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := sequence.Add(1)
		logger.Printf("%s: access started request=%d method=%s path=%q host=%q remote=%q forwarded_for=%q content_length=%d",
			component, requestID, r.Method, r.URL.EscapedPath(), r.Host, r.RemoteAddr,
			r.Header.Get("X-Forwarded-For"), r.ContentLength)
		metrics := httpsnoop.CaptureMetrics(next, w, r)
		logger.Printf("%s: access completed request=%d method=%s path=%q status=%d bytes=%d duration=%s",
			component, requestID, r.Method, r.URL.EscapedPath(), metrics.Code, metrics.Written, metrics.Duration)
	})
}
