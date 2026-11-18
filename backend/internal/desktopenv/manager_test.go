package desktopenv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOIDCMissingRoleReachesDesktop(t *testing.T) {
	environment := &Environment{ID: "one", Kind: "remote", BaseURL: "https://brigade.example"}
	m := &Manager{config: config{Environments: []*Environment{environment}}}
	params := url.Values{"environment_id": {"one"}, "error": {"oidc_missing_role"}, "role": {"team:<user>"}, "return_to": {"/desktop/oidc/done"}}
	w := httptest.NewRecorder()
	m.OIDCCallbackHandler(w, httptest.NewRequest(http.MethodGet, "/desktop/oidc/callback?"+params.Encode(), nil))
	items, _, _ := m.List()
	if !strings.Contains(items[0].Error, "«team:<user>»") {
		t.Fatalf("missing role in desktop login error: %q", items[0].Error)
	}
	done := httptest.NewRecorder()
	m.OIDCDoneHandler(done, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
	if !strings.Contains(done.Body.String(), "team:&lt;user&gt;") {
		t.Fatalf("missing escaped role on completion page: %s", done.Body.String())
	}
	m.OIDCStartHandler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/desktop/oidc/start?environment_id=one", nil))
	items, _, _ = m.List()
	if items[0].Error != "" {
		t.Fatalf("stale error on retry: %q", items[0].Error)
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		valid bool
	}{
		{"https://brigade.example.com/", "https://brigade.example.com", true},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://brigade.example.com", "", false},
		{"https://user:pass@brigade.example.com", "", false},
		{"https://brigade.example.com/path", "", false},
	} {
		got, err := normalizeRemoteURL(test.input)
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("normalize %q = %q, %v", test.input, got, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("normalize %q unexpectedly succeeded", test.input)
		}
	}
}

func TestShouldProxy(t *testing.T) {
	for path, want := range map[string]bool{
		"/brigade.v1.SessionService/List":             true,
		"/brigade.v1.DesktopService/ListEnvironments": false,
		"/api/ag-ui/run":                              true,
		"/ws/terminal/session":                        true,
		"/settings":                                   false,
	} {
		if got := shouldProxy(path); got != want {
			t.Errorf("shouldProxy(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDetachResourcesRotatesContext(t *testing.T) {
	old, cancel := context.WithCancel(context.Background())
	m := &Manager{resourceContext: old, resourceCancel: cancel}
	m.detachResourcesLocked()
	select {
	case <-old.Done():
	default:
		t.Fatal("old resource context was not canceled")
	}
	select {
	case <-m.resourceContext.Done():
		t.Fatal("new resource context is already canceled")
	default:
	}
}
