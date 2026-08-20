package connectsvc

import (
	"encoding/json"
	"net/http"

	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/plugin"
)

type pluginTokenVerifier interface {
	Verify(token string) (userID string, ok bool)
}

func PluginUploadHandler(verifier pluginTokenVerifier, manager *plugin.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := verifier.Verify(auth.AccessTokenFromRequest(r))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		installed, err := manager.InstallReaderFor(r.Context(), userID, r.Header.Get("X-Filename"), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": installed.ID})
	})
}
