package linkpreview

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGetParsesOpenGraphAndCaches(t *testing.T) {
	requests := 0
	cache, _ := lru.New[string, Preview](1024)
	svc := &Service{cache: cache, client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		body := `<html><head><title>Fallback</title><meta property="og:title" content="Brigade"><meta property="og:description" content="Coding agents"><meta property="og:image" content="/cover.png"><link rel="icon" href="/favicon.ico"></head></html>`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}

	for range 2 {
		preview, err := svc.Get(context.Background(), "https://example.com/page")
		if err != nil {
			t.Fatal(err)
		}
		if preview.Title != "Brigade" || preview.Description != "Coding agents" || preview.ImageURL != "https://example.com/cover.png" || preview.IconURL != "https://example.com/favicon.ico" {
			t.Fatalf("preview = %+v", preview)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("%s разрешён", raw)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Error("публичный адрес запрещён")
	}
}

func TestValidateURLRejectsPrivateLiteral(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1/secret")
	if err != nil {
		t.Fatal(err)
	}
	if validateURL(u) == nil {
		t.Fatal("private URL is allowed")
	}
}
