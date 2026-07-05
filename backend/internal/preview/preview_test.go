package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

const testSessionID = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"

func testConfig() Config {
	return Config{
		Enabled:    true,
		Domain:     "localhost",
		Scheme:     "http",
		ListenPort: 10000,
	}
}

func TestPublicURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		port int
		want string
	}{
		{
			name: "local dev with listener port",
			cfg:  testConfig(),
			port: 3000,
			want: "http://" + testSessionID + "-3000.localhost:10000",
		},
		{
			name: "https on standard port omits it",
			cfg:  Config{Enabled: true, Domain: "brigade.example.com", Scheme: "https", ExternalPort: 443},
			port: 8080,
			want: "https://" + testSessionID + "-8080.brigade.example.com",
		},
		{
			name: "external port overrides listener",
			cfg:  Config{Enabled: true, Domain: "example.com", Scheme: "https", ExternalPort: 8443, ListenPort: 10000},
			port: 3000,
			want: "https://" + testSessionID + "-3000.example.com:8443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.PublicURL(testSessionID, tt.port); got != tt.want {
				t.Fatalf("PublicURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestURLTemplate(t *testing.T) {
	got := testConfig().URLTemplate(testSessionID)
	want := "http://" + testSessionID + "-{port}.localhost:10000"
	if got != want {
		t.Fatalf("URLTemplate = %q, want %q", got, want)
	}
}

func TestTokens(t *testing.T) {
	s := NewService(testConfig(), []byte("secret"))
	token := s.TokenFor(testSessionID)
	if token == "" {
		t.Fatal("empty token")
	}
	if !s.VerifyToken(testSessionID, token) {
		t.Fatal("valid token rejected")
	}
	if s.VerifyToken(testSessionID, token+"x") {
		t.Fatal("tampered token accepted")
	}
	if s.VerifyToken("другая-сессия", token) {
		t.Fatal("token accepted for another session")
	}
	// Детерминизм: тот же секрет — тот же токен (переживает рестарт).
	if s2 := NewService(testConfig(), []byte("secret")); s2.TokenFor(testSessionID) != token {
		t.Fatal("token is not deterministic")
	}
}

func TestRegisterUpsertAndDrop(t *testing.T) {
	s := NewService(testConfig(), []byte("secret"))
	s.Register(testSessionID, 8080, "api")
	s.Register(testSessionID, 3000, "vite")
	s.Register(testSessionID, 3000, "vite-2") // upsert по порту

	list := s.List(testSessionID)
	if len(list) != 2 {
		t.Fatalf("got %d previews, want 2", len(list))
	}
	if list[0].Port != 3000 || list[0].Name != "vite-2" || list[1].Port != 8080 {
		t.Fatalf("unexpected list: %+v", list)
	}

	s.Drop(testSessionID)
	if got := s.List(testSessionID); len(got) != 0 {
		t.Fatalf("list after drop: %+v", got)
	}
}

func TestRegisterHandler(t *testing.T) {
	s := NewService(testConfig(), []byte("secret"))
	mux := http.NewServeMux()
	mux.Handle("POST /api/preview/{sessionId}/register", s.RegisterHandler())

	do := func(token string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/preview/"+testSessionID+"/register", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if w := do("wrong", map[string]any{"port": 3000}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: code %d, want 401", w.Code)
	}
	if w := do(s.TokenFor(testSessionID), map[string]any{"port": 0}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad port: code %d, want 400", w.Code)
	}

	w := do(s.TokenFor(testSessionID), map[string]any{"port": 3000, "name": "vite"})
	if w.Code != http.StatusOK {
		t.Fatalf("register: code %d, body %s", w.Code, w.Body)
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if want := testConfig().PublicURL(testSessionID, 3000); resp["url"] != want {
		t.Fatalf("url = %q, want %q", resp["url"], want)
	}
	if len(s.List(testSessionID)) != 1 {
		t.Fatal("preview was not registered")
	}
}

// fakeSessions — SessionSource для тестов резолвера и прокси.
type fakeSessions struct {
	sessions map[string]store.Session
}

func (f *fakeSessions) GetSession(_ context.Context, id string) (store.Session, error) {
	sess, ok := f.sessions[id]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return sess, nil
}

type fakeIPs struct {
	ip    string
	calls int
}

func (f *fakeIPs) ContainerIP(context.Context, string, string) (string, error) {
	f.calls++
	return f.ip, nil
}

func TestResolver(t *testing.T) {
	sessions := &fakeSessions{sessions: map[string]store.Session{
		testSessionID: {ID: testSessionID, Mode: store.SessionModeLocal, Status: store.SessionStatusRunning},
		"d0c0e5e1-0000-4000-8000-000000000001": {
			ID: "d0c0e5e1-0000-4000-8000-000000000001", Mode: store.SessionModeDocker,
			Status: store.SessionStatusRunning,
		},
		"57019bed-0000-4000-8000-000000000002": {
			ID: "57019bed-0000-4000-8000-000000000002", Mode: store.SessionModeLocal,
			Status: store.SessionStatusStopped,
		},
	}}
	ips := &fakeIPs{ip: "172.17.0.5"}
	r := NewResolver(sessions, ips)

	u, err := r.Resolve(context.Background(), testSessionID, 3000)
	if err != nil || u.String() != "http://127.0.0.1:3000" {
		t.Fatalf("local: %v %v", u, err)
	}

	u, err = r.Resolve(context.Background(), "d0c0e5e1-0000-4000-8000-000000000001", 8080)
	if err != nil || u.String() != "http://172.17.0.5:8080" {
		t.Fatalf("docker: %v %v", u, err)
	}
	// Повторный резолв в пределах TTL — из кэша.
	_, _ = r.Resolve(context.Background(), "d0c0e5e1-0000-4000-8000-000000000001", 8081)
	if ips.calls != 1 {
		t.Fatalf("container ip calls = %d, want 1 (cache)", ips.calls)
	}
	r.Invalidate("d0c0e5e1-0000-4000-8000-000000000001")
	_, _ = r.Resolve(context.Background(), "d0c0e5e1-0000-4000-8000-000000000001", 8081)
	if ips.calls != 2 {
		t.Fatalf("container ip calls after invalidate = %d, want 2", ips.calls)
	}

	if _, err := r.Resolve(context.Background(), "57019bed-0000-4000-8000-000000000002", 80); err == nil {
		t.Fatal("stopped session resolved")
	}
	if _, err := r.Resolve(context.Background(), "ffffffff-0000-4000-8000-00000000000f", 80); err == nil {
		t.Fatal("unknown session resolved")
	}
}

func TestWrapRouting(t *testing.T) {
	// Upstream — настоящий HTTP-сервер: проверяем сквозное проксирование.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte("hello from dev server"))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	_, portStr, _ := splitHostPortForTest(u.Host)

	sessions := &fakeSessions{sessions: map[string]store.Session{
		testSessionID: {ID: testSessionID, Mode: store.SessionModeLocal, Status: store.SessionStatusRunning},
	}}
	r := NewResolver(sessions, nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // маркер «не прокси»
	})
	h := Wrap(testConfig(), r, next)

	serve := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// Preview-хост → проксируется в upstream.
	if w := serve(testSessionID + "-" + portStr + ".localhost:10000"); w.Code != http.StatusOK ||
		w.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("proxy: code %d, body %s", w.Code, w.Body)
	}
	// Обычный хост → в next.
	if w := serve("localhost:10000"); w.Code != http.StatusTeapot {
		t.Fatalf("plain host: code %d, want 418", w.Code)
	}
	// Поддомен без preview-формата → в next.
	if w := serve("foo.localhost:10000"); w.Code != http.StatusTeapot {
		t.Fatalf("non-preview subdomain: code %d, want 418", w.Code)
	}
	// Несуществующая сессия → 404.
	if w := serve("ffffffff-0000-4000-8000-00000000000f-3000.localhost:10000"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown session: code %d, want 404", w.Code)
	}
}

func splitHostPortForTest(hostport string) (string, string, error) {
	host := SplitHostPort(hostport)
	port := hostport[len(host)+1:]
	return host, port, nil
}

func cookieConfig() Config {
	return Config{
		Enabled:      true,
		Mode:         "cookie",
		CookieHost:   "preview.example.com",
		Scheme:       "https",
		ExternalPort: 443,
	}
}

func TestCookiePublicURL(t *testing.T) {
	got := cookieConfig().PublicURL(testSessionID, 3000)
	want := "https://preview.example.com/?id=" + testSessionID + "-3000"
	if got != want {
		t.Fatalf("cookie PublicURL = %q, want %q", got, want)
	}
	tmpl := cookieConfig().URLTemplate(testSessionID)
	wantTmpl := "https://preview.example.com/?id=" + testSessionID + "-{port}"
	if tmpl != wantTmpl {
		t.Fatalf("cookie URLTemplate = %q, want %q", tmpl, wantTmpl)
	}
}

func TestWrapCookie(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("dev:" + r.URL.Path))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	_, portStr, _ := splitHostPortForTest(u.Host)

	sessions := &fakeSessions{sessions: map[string]store.Session{
		testSessionID: {ID: testSessionID, Mode: store.SessionModeLocal, Status: store.SessionStatusRunning},
	}}
	r := NewResolver(sessions, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := Wrap(cookieConfig(), r, next)

	id := testSessionID + "-" + portStr

	// 1) ?id=... → Set-Cookie + 302 на "/"
	req := httptest.NewRequest(http.MethodGet, "https://x/?id="+id, nil)
	req.Host = "preview.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("id query: code %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("redirect to %q, want /", loc)
	}
	var setCookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			setCookie = c.Value
		}
	}
	if setCookie != id {
		t.Fatalf("cookie value %q, want %q", setCookie, id)
	}

	// 2) cookie → proxy к upstream (корневой и абсолютный путь)
	for _, path := range []string{"/", "/assets/app.js"} {
		req = httptest.NewRequest(http.MethodGet, "https://x"+path, nil)
		req.Host = "preview.example.com"
		req.AddCookie(&http.Cookie{Name: cookieName, Value: id})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "dev:"+path {
			t.Fatalf("proxy %s: code %d body %q", path, w.Code, w.Body.String())
		}
	}

	// 3) нет cookie → заглушка (200, не проксирование, не next)
	req = httptest.NewRequest(http.MethodGet, "https://x/", nil)
	req.Host = "preview.example.com"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Code == http.StatusTeapot {
		t.Fatalf("no cookie: code %d, want 200 заглушка", w.Code)
	}

	// 4) чужой хост → next
	req = httptest.NewRequest(http.MethodGet, "https://x/", nil)
	req.Host = "brigade.example.com"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTeapot {
		t.Fatalf("other host: code %d, want 418 (next)", w.Code)
	}

	// 5) невалидный id в query → 400
	req = httptest.NewRequest(http.MethodGet, "https://x/?id=garbage", nil)
	req.Host = "preview.example.com"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: code %d, want 400", w.Code)
	}
}
