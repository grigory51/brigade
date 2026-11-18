package connectsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/grigory51/brigade/backend/internal/auth"
)

func TestOIDCFailureRedirect(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":"http://%s"}`, r.Host)
	}))
	defer provider.Close()
	const role = "corp:brigade & users"
	login, err := auth.NewOIDC(context.Background(), nil, auth.OIDCConfig{Issuer: provider.URL, RequiredRole: role})
	if err != nil {
		t.Fatal(err)
	}
	service := &AuthService{oidc: login}
	for _, tc := range []struct {
		err        error
		code, role string
	}{
		{auth.ErrRequiredRole, "oidc_missing_role", role},
		{auth.ErrInvalidToken, "oidc_invalid_request", ""},
		{errors.New("private provider response"), "oidc", ""},
	} {
		for _, dest := range []string{"/login", "http://127.0.0.1:8787/desktop/oidc/callback?environment_id=test"} {
			w := httptest.NewRecorder()
			service.redirectOIDCError(w, httptest.NewRequest(http.MethodGet, "/", nil), dest, tc.err)
			target, err := url.Parse(w.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			query := target.Query()
			if w.Code != http.StatusFound || query.Get("error") != tc.code || query.Get("role") != tc.role {
				t.Fatalf("redirect for %v: %s", tc.err, target)
			}
			query.Del("error")
			query.Del("role")
			target.RawQuery = query.Encode()
			if target.String() != dest {
				t.Fatalf("callback changed: %s", target)
			}
		}
	}
}

func TestOIDCRedirectsStayOnTrustedOrigins(t *testing.T) {
	if got := safeReturnTo("//evil.example/path"); got != "/sessions" {
		t.Fatalf("protocol-relative return accepted: %q", got)
	}
	if got := safeReturnTo("/settings?section=agents"); got != "/settings?section=agents" {
		t.Fatalf("local return rejected: %q", got)
	}
	if !validDesktopCallback("http://127.0.0.1:8787/desktop/oidc/callback?environment_id=one") {
		t.Fatal("Brigade.app callback rejected")
	}
	if validDesktopCallback("http://127.0.0.1:9999/desktop/oidc/callback") {
		t.Fatal("foreign loopback port accepted")
	}
}
