// Package web встраивает собранный фронтенд (web/dist) в бинарь через go:embed
// и отдаёт его как SPA: статика по совпадению пути, остальное — fallback на index.html.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist содержит собранный Vite-фронтенд. Каталог dist обязан существовать на момент
// сборки (его наполняет `npm run build` в web/); placeholder с index.html гарантирует
// компилируемость до первой реальной сборки фронта.
//
//go:embed all:dist
var dist embed.FS

// Handler возвращает http.Handler, отдающий встроенный SPA-фронтенд.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	return spaHandler{root: sub}, nil
}

type spaHandler struct {
	root fs.FS
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}

	// Существующий файл отдаём как есть; иначе — index.html (клиентский роутинг SPA).
	if f, err := h.root.Open(name); err == nil {
		_ = f.Close()
		http.FileServer(http.FS(h.root)).ServeHTTP(w, r)
		return
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	http.FileServer(http.FS(h.root)).ServeHTTP(w, r2)
}
