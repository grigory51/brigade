package httpaccess

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogOmitsQueryAndCapturesResponse(t *testing.T) {
	var output bytes.Buffer
	handler := wrap(log.New(&output, "", 0), "brigade", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	request := httptest.NewRequest(http.MethodPost, "https://brigade.example/api/run?ticket=secret", strings.NewReader("body"))
	request.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	got := output.String()
	for _, want := range []string{"access started", `method=POST`, `path="/api/run"`, "status=201", "bytes=2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "ticket") || strings.Contains(got, "Authorization") {
		t.Fatalf("access log contains credentials: %q", got)
	}
}
