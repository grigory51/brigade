// Package filedownload отдаёт созданные агентом файлы из workspace сессии.
package filedownload

import (
	"context"
	"mime"
	"net/http"
	"os"

	"github.com/grigory51/brigade/backend/internal/auth"
)

type tokenVerifier interface {
	Verify(token string) (userID string, ok bool)
}

type fileProvider interface {
	OpenWorkspaceFile(ctx context.Context, sessionID, userID, name string) (*os.File, error)
}

// Handler собирает authenticated download-ручку. Проверку владельца и безопасное открытие
// пути выполняет provider; наружу любая ошибка файла выглядит как 404.
func Handler(verifier tokenVerifier, files fileProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := verifier.Verify(auth.AccessTokenFromRequest(r))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		f, err := files.OpenWorkspaceFile(r.Context(), r.PathValue("sessionId"), userID, r.PathValue("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}

		disposition := "attachment"
		if r.URL.Query().Has("inline") {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Name()}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}
